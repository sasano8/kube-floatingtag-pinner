# kube-floatingtag-pinner（設計メモ。第一段階は実装済み・[README.md](./README.md) 参照）

annotation で対象を指定したワークロード（Deployment / StatefulSet / DaemonSet / Knative Service）の
イメージを定期監視し、`latest` などの可変タグの指す実体（digest）が変わったら、新しいイメージで
動かし直す自作の仕組み。第一段階（収集・差分検知・pull）は実装済みで、このメモは未実装の部分（第二段階以降）を中心に残す。

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

- Deployment と ksvc で仕組みを分けず、**1つの resolver に統一**する（Image Updater との二重化を避ける）。

## 第一段階の残課題

実装済みの範囲は [README.md](./README.md) を参照（収集、リモートの digest 取得、ローカルキャッシュ（ノードのイメージストア）との比較、
差分時の pull、ログ出力、`once` / `run`）。残っているのは次の点。

- 実機での確認: pull 用 Pod（存在しないコマンドで kubelet に pull だけさせる）が、使っているランタイムで期待どおり動くか。
- レジストリ認証（非公開レジストリ）と、rate limit（Docker Hub の匿名アクセスの上限）の確認。
- 結果の出力先の追加: Event / メトリクス（現状はログのみ）。

## 第二段階: 反映

差分を検知したら（ローカルキャッシュの digest と、リソースの image 指定の食い違い。実行中の Pod / Revision の digest は見ない。[README.md](./README.md) 参照）、spec の image に digest を書く。

- 書き方: `image: repo:latest@sha256:<新しい digest>`（kubelet は digest があるとタグを無視する。`latest` を残せば読みやすい）。
- ksvc: image 変更で新しい Revision ができ、その digest で固定される。
- Deployment: Pod テンプレートの変更でローリングが走り、全 Pod が同一 digest になる。
- annotation にだけ digest を入れる案は、Deployment では **Pod ごとにずれるため不採用**
  （image 欄が `latest` のままのため）。
- 書き込み要否は、spec の image 指定とローカルキャッシュの digest の比較で決める（Pod の `imageID` / Revision の `imageDigest` は見ない）。

## 第三段階: Argo CD との折り合い（第二段階の実運用前に必要）

image 欄は Argo CD の管理下にあるため、自作 resolver が書くと `selfHeal` で戻される。

- 案1（自作で統一するなら本命）: Application に `ignoreDifferences`（image 欄の jqPathExpressions、または
  resolver 専用の field manager 名を `managedFieldsManagers` で指定）と `syncOptions: RespectIgnoreDifferences=true`。
  欠点: 変更が git に残らない。git 側で image を変えても反映されなくなる。
- 案2: Application の `kustomize.images` に書く（Image Updater の `argocd` 方式）。衝突しないが Kustomize 化が必要で、実質 Image Updater。

## 未確認事項

- 既存ツールの調査結果は [docs/research/existing-tools.md](./docs/research/existing-tools.md) に移した（Keel は ksvc に使えない見込み、Knative 本体は `latest` に追従しない）。
- `ignoreDifferences` / `managedFieldsManagers` が、この組み合わせ（ksvc・Deployment の image 欄）で効くか。
- ksvc の `image` を `latest@sha256:...` に書き換えたときの挙動（新 Revision の作成、digest 固定）。
- Revision を消すと Configuration が再作成するか（配信中は断が出うるので使わない方針）。

## 既存の関連物

- [plugins/argocd-image-updater](../argocd-image-updater)（commit `cbcc513`）: Argo CD Image Updater の導入。
  `digest` 戦略 + `argocd` 書き戻しで、Helm / Kustomize のソースだけを更新できる。
  本 resolver に統一する場合は不要になる（外す／保留にするかは第二段階の後に判断）。
- `apps/tutorial/manifests/kustomization.yaml` / `apps/tutorial/image-updater.yaml`: Image Updater 用。未コミット。
- 検証用の ksvc は `apps/tutorial/manifests/hello-ksvc-*.yml`（`hello-ksvc-grpc.yml` / `hello-ksvc-websocket.yml` は
  `:latest` を明記済み・未コミット）。第一段階の動作確認では、ここに annotation を付けて試せる。
- 関連する検討: [plugins/reloader](../reloader)（ConfigMap / Secret の変更でローリング再起動。イメージ更新とは別）。
