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
差分時の pull、ログ出力、1回実行と `--daemon`）。残っているのは次の点。

- 実機での確認: pull 用 Pod（存在しないコマンドで kubelet に pull だけさせる）が、使っているランタイムで期待どおり動くか。
- レジストリ認証（非公開レジストリ）と、rate limit（Docker Hub の匿名アクセスの上限）の確認。
- 結果の出力先の追加: Event / メトリクス（現状はログのみ）。

## 第二段階: アクション（組み込みは再デプロイ）

spec の image は書き換えない（ユーザーの管轄のものにはあまり触らない）。実行中のものが古ければ、再デプロイして
kubelet / Knative に新しいイメージを取り直させる。リモートレジストリは登場しない（pull は第一段階が済ませている）。

- 差分の検知: ローカルキャッシュの digest と、実行中の Pod の `status.containerStatuses[].imageID`
  （ksvc は Revision の `status.containerStatuses[].imageDigest`）を比べ、不一致なら再デプロイする。
  実行中のものを見るので、ツールが止まっていた間の変化や、最初から古かったリソースも拾える。
- 操作（種類ごと）:
  - Deployment / StatefulSet / DaemonSet: Pod を削除する（spec を触らない）。または Pod テンプレートに
    `restartedAt` の annotation を付けて段階的に入れ替える。
  - Knative Service: Pod を消しても同じ digest で作り直されるだけ（Revision は不変）。ksvc の `spec.template` に
    annotation を足して新しい Revision を作る（タグはそのときに解決し直される）。
- どの操作をするかは「アクション」として切り離し、annotation で指定する（案: `floatingtag-pinner/action: info | none | delete-pods | restart | new-revision`）。
  未指定は `info`（差分をログに出すだけ）。コマンドは `pinner trigger`。詳細は [docs/design.md](./docs/design.md)。
- 前提: 再デプロイで新しいイメージを使うには、ノードの `latest` タグが新しいイメージを指している必要がある
  （第一段階の pull をタグ指定にしている理由）。

### 採らなかった案: spec の image に digest を書く（`repo:latest@sha256:…`）

困るケースが多いため、採らない。

- マニフェストを push / `kubectl apply` するたびに `latest` に戻り、ローリングが走り、ツールがまた書き込む（行き来）。
- Argo CD / Flux の selfHeal に戻される。Helm の upgrade に上書きされる。server-side apply の field manager が衝突する。
- git とクラスタが食い違い、git が正本でなくなる。spec は `latest` と読めるのに実際は固定された digest で動く。
- ツールが止まると、固定された古い digest のまま残り、新しい Pod もそれを使う。
- Kyverno / OPA のポリシー（タグ必須、署名検証）に弾かれうる。
- Knative で、`latest@sha256:…` への書き換えが期待どおり新 Revision を作るか未確認。

## 第三段階: Argo CD との折り合い（第二段階の実運用前に確認）

image を書き換えないので、selfHeal との衝突は大きく減る。ただし次を確認する。

- Pod の削除は spec を変えないので影響しない見込み。
- テンプレートへの annotation（`restartedAt` や ksvc の template annotation）を、Argo CD が差分として戻さないか。
  戻す場合は `ignoreDifferences` を使う。

## 未確認事項

- 調査結果は [docs/research.md](./docs/research.md) に移した（Keel は ksvc に使えない見込み、Knative 本体は `latest` に追従しない、rate limit、Revision の削除）。
- ksvc の `spec.template` に annotation を足したとき、新しい Revision ができ、そのとき `latest` が解決し直されること。
- Revision を消すと Configuration が再作成するか → **調査済み: 最新の Revision は作り直される見込み**（ソースから。実機は未確認）。
  ただし配信中は断が出うるので使わない方針は変えない（[docs/research.md](./docs/research.md) の「Revision を消すとどうなるか」）。

## 既存の関連物

- [plugins/argocd-image-updater](../argocd-image-updater)（commit `cbcc513`）: Argo CD Image Updater の導入。
  `digest` 戦略 + `argocd` 書き戻しで、Helm / Kustomize のソースだけを更新できる。
  本 resolver に統一する場合は不要になる（外す／保留にするかは第二段階の後に判断）。
- `apps/tutorial/manifests/kustomization.yaml` / `apps/tutorial/image-updater.yaml`: Image Updater 用。未コミット。
- 検証用の ksvc は `apps/tutorial/manifests/hello-ksvc-*.yml`（`hello-ksvc-grpc.yml` / `hello-ksvc-websocket.yml` は
  `:latest` を明記済み・未コミット）。第一段階の動作確認では、ここに annotation を付けて試せる。
- 関連する検討: [plugins/reloader](../reloader)（ConfigMap / Secret の変更でローリング再起動。イメージ更新とは別）。
