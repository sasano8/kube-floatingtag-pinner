# kube-floatingtag-pinner（未実装・設計済み）

annotation で対象を指定したワークロード（Deployment / StatefulSet / DaemonSet / Knative Service）の
イメージを定期監視し、`latest` などの可変タグの指す実体（digest）が変わったら、新しいイメージで
動かし直す自作の仕組み。第一段階（収集・変化検知）から実装する。

## 背景

- 開発中は `image: xxx:latest` で参照し、レジストリ上の `latest` の実体が更新されたら、それを
  pull し直して使いたい。バージョン番号の追従は目的にしない（タグ無し・ハッシュのみの運用のイメージもある）。
- Argo CD で `apps/*/app.yaml` を管理している。`selfHeal: true`。

### 問題

1. git も live の ksvc / Deployment も `:latest` のままなので、Argo CD から見て**差分が出ない**（Synced のまま）。
2. Knative は **Revision 作成時にタグを digest に解決して固定**する。Revision は不変なので、
   `latest` に push しても既存 Revision は変わらない。新しいイメージを使うには ksvc の spec を変えて
   新しい Revision を作る必要がある。
3. Deployment も、spec が `:latest` の文字列のままだと、Pod 起動ごとにノードが解決するため、
   ローリング中・スケールアウト中にレジストリが更新されると**Pod 間で digest がずれる**。
4. Argo CD が管理していない場所（live）を直接 patch すると、`selfHeal` で元に戻される。

### `latest` の指す先がどこにあるか

- **レジストリ**: 正本。タグ → digest の対応はここだけが持つ。
- **各ノードのコンテナランタイムのイメージストア**（containerd 等）: pull したイメージのローカルキャッシュ。
  ノードごとに別々で、古い digest が残りうる。Node の `status.images[].names` に載る（件数上限あり、kubelet 既定50件）。
- **Kubernetes の API オブジェクト（spec）**: `image: x:latest` という文字列だけ。
  例外は Pod の `status.containerStatuses[].imageID`（その Pod が起動時に使った digest）と、
  Revision の `status.containerStatuses[].imageDigest`。
- pull するのはノード（kubelet）。resolver がやるのはレジストリへ digest を問い合わせることだけ（イメージ本体は落とさない）。
- ノードのキャッシュからイメージを消しても、動いている Pod は影響を受けない。次の Pod 作成時（`Always` なら毎回）に反映される。
  更新のきっかけは**spec の変更**で、ノードのキャッシュ操作は不要。

## 方針（決定済み）

- 対象は annotation で宣言する（例: `image-resolver/enabled: "true"`）。ワークロード自身の
  `metadata.annotations` に置く（Revision には伝わらない）。
- Deployment と ksvc で仕組みを分けず、**1つの resolver に統一**する（Image Updater との二重化を避ける）。
- 手段は最小から始める: CronJob + `crane` + `kubectl` の bash。必要になったらコントローラ化（kopf / shell-operator / kubebuilder 等）。
- 置き場所は `plugins/image-resolver/`（[plugins/argocd](../argocd) 等と同じ規約。`scripts/plugin-install.sh image-resolver` で入れられる形にする）。

## 第一段階: 収集と変化検知（読み取りのみ）

ワークロードを一切変更しない。Argo CD との衝突も起きない。

1. `image-resolver/enabled: "true"` が付いた Deployment / StatefulSet / DaemonSet / ksvc を一覧する。
2. Pod テンプレートの `containers[].image` / `initContainers[].image`（ksvc は `spec.template.spec.containers[].image`）を取り出し、重複を除く。
3. イメージごとにレジストリの digest を取る（`crane digest <image>`）。定期実行。
4. 前回の digest（ConfigMap などに保存）と比べ、**変わったイメージ**を検知する。
5. 結果を出力する: イメージ、参照元リソース、前回と今回の digest、変化の有無
   （ConfigMap / ログ / Event / メトリクスのいずれか）。

この段階で確かめること: annotation による対象の拾い方、レジストリ認証（非公開レジストリ）、
rate limit（Docker Hub の匿名アクセスの上限）、重複除去。

## 第二段階: 反映

変化を検知したら、spec の image に digest を書く。

- 書き方: `image: repo:latest@sha256:<新しい digest>`（kubelet は digest があるとタグを無視する。`latest` を残せば読みやすい）。
- ksvc: image 変更で新しい Revision ができ、その digest で固定される。
- Deployment: Pod テンプレートの変更でローリングが走り、全 Pod が同一 digest になる。
- annotation にだけ digest を入れる案は、Deployment では **Pod ごとにずれるため不採用**
  （image 欄が `latest` のままのため）。
- 補強として、書き込み要否の判断に Pod の `imageID` / Revision の `imageDigest` との突き合わせを足す
  （実際に古いか）。Node の `status.images` も参考にできるが必須ではない。

## 第三段階: Argo CD との折り合い（第二段階の実運用前に必要）

image 欄は Argo CD の管理下にあるため、自作 resolver が書くと `selfHeal` で戻される。

- 案1（自作で統一するなら本命）: Application に `ignoreDifferences`（image 欄の jqPathExpressions、または
  resolver 専用の field manager 名を `managedFieldsManagers` で指定）と `syncOptions: RespectIgnoreDifferences=true`。
  欠点: 変更が git に残らない。git 側で image を変えても反映されなくなる。
- 案2: Application の `kustomize.images` に書く（Image Updater の `argocd` 方式）。衝突しないが Kustomize 化が必要で、実質 Image Updater。

## 未確認事項

- `ignoreDifferences` / `managedFieldsManagers` が、この組み合わせ（ksvc・Deployment の image 欄）で効くか。
- ksvc の `image` を `latest@sha256:...` に書き換えたときの挙動（新 Revision の作成、digest 固定）。
- Revision を消すと Configuration が再作成するか（配信中は断が出うるので使わない方針）。
- Keel が ksvc に使えるか → **調査済み: 使えない見込み**（[README.md](./README.md) の「既存ツールの対応状況」参照）。
- Knative 本体に `latest` 自動追従の機能が無いこと → **調査済み: 無い見込み**（[README.md](./README.md) の「既存ツールの対応状況」参照）。

## 既存の関連物

- [plugins/argocd-image-updater](../argocd-image-updater)（commit `cbcc513`）: Argo CD Image Updater の導入。
  `digest` 戦略 + `argocd` 書き戻しで、Helm / Kustomize のソースだけを更新できる。
  本 resolver に統一する場合は不要になる（外す／保留にするかは第二段階の後に判断）。
- `apps/tutorial/manifests/kustomization.yaml` / `apps/tutorial/image-updater.yaml`: Image Updater 用。未コミット。
- 検証用の ksvc は `apps/tutorial/manifests/hello-ksvc-*.yml`（`hello-ksvc-grpc.yml` / `hello-ksvc-websocket.yml` は
  `:latest` を明記済み・未コミット）。第一段階の動作確認では、ここに annotation を付けて試せる。
- 関連する検討: [plugins/reloader](../reloader)（ConfigMap / Secret の変更でローリング再起動。イメージ更新とは別）。
