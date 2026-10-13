# 調査メモ

このプロジェクトに関連するものを調べた結果。各節に、**どこまで確認できたか**（ソース・ドキュメントで確認／実機で確認）を書く。
設計への反映は [design.md](./design.md)、決まった方針は [README.md](../README.md) を参照。

## 1. 既存ツールの対応状況

調べた範囲では、ksvc の `latest` 追従を満たす既存ツールは見つかっていない。実機で確認済みなのは Knative 本体のみ（下記）。

| ツール | Deployment 等 | ksvc | 備考 |
| --- | :---: | :---: | --- |
| Keel | ○ | ×（見込み） | `policy: force` + `matchTag` で可変タグの digest 変化を検知できるが、対象は標準ワークロードと Helm のみ |
| Knative 本体 | - | ×（実機で確認） | タグ解決は Revision 作成時の1回のみ。追従機能は無い |
| Argo CD Image Updater | △ | △ | `digest` 戦略 + `argocd` 書き戻しで、Helm / Kustomize のソースのみ更新可。導入済みだが未稼働のため未検証 |

### Keel（ksvc に使えるか）

結論: **ksvc には使えない見込み**（公式ドキュメント・ソースの範囲での確認。実機では未検証）。

- Keel の対象は Helm / DaemonSet / StatefulSet / Deployment。Knative Service の記載は見当たらない
  （[keel-hq/keel](https://github.com/keel-hq/keel)）。
- provider は `kubernetes`（標準ワークロードのみ）と `helm3` の2つで、Knative 用は無い
  （[provider/](https://github.com/keel-hq/keel/tree/master/provider)。一覧は表示上すべてとは限らない）。
- 可変タグ（`latest`）の追従は可能: `keel.sh/policy: force` + `keel.sh/matchTag: "true"` で、
  ポーリング時にマニフェストの digest を比較して検知できる。ただし更新先は上記の標準ワークロードに限られる。
- よって Deployment 等は Keel で足りる可能性があるが、ksvc は別手段が要る。
  「Deployment と ksvc を1つの仕組みに統一する」方針（[IDEA.md](../IDEA.md) 参照）の根拠が補強される。

### Knative 本体（`latest` 自動追従の有無）

結論: **Knative 本体に `latest` への自動追従は無い**（ソース・Issue に加え、実機でも確認）。

- 実機の検証結果: `app-authorization-demo` のイメージを更新して `latest` に push したが、
  `latest` を参照する ksvc は約20分経っても最新のイメージに更新されなかった（新 Revision もできない）。
  検証時は Argo CD Image Updater を動かしていない（Knative 本体のみの挙動）。
- 確認した値（2026-10-12）: `ghcr.io/sasano8/app-authorization-demo:latest` を参照する最新の Revision
  `app-authorization-demo-00007`（06:46 UTC 作成）の `status.containerStatuses[0].imageDigest` は
  `sha256:4a45bcf8…` のまま。レジストリ上の `latest` の現在の digest は `sha256:bf16c8e0…`
  （`docker buildx imagetools inspect` で取得）で、**一致しない**。
  Revision 00005 / 00006 / 00007 は、それぞれ作成時点の `latest` の digest（`fdb79066…` / `f84b09ad…` / `4a45bcf8…`）で
  固定されており、Revision を作った時にだけ解決される挙動とも合う。
- ノード側の確認: ノード `bootstrapoci` の `status.images` には `app-authorization-demo` が
  `@sha256:4a45bcf8…`（Revision 00007）と `@sha256:f84b09ad…`（00006）の2件だけあり、
  レジストリ上の最新 `bf16c8e0…` は**無い**。名前はどちらも digest 付きで、`:latest` のタグ名では載っていない
  （Revision が digest で pull するため）。つまり最新のイメージはノードにも pull されていない。
  ただし `status.images` は kubelet 既定で50件までなので、無いことの確証には `crictl images` 等での確認が要る。
- 最新 digest の確認: `crane digest <image>:latest`、または `docker buildx imagetools inspect <image>:latest`。
  Revision 側は `kubectl get revision -o custom-columns=NAME:.metadata.name,CREATED:.metadata.creationTimestamp,DIGEST:.status.containerStatuses[0].imageDigest`。

- タグ → digest の解決は、リゾルバが `remote.Head` を1回呼ぶだけ。ループ・タイマー・キャッシュ・watch は無く、
  タグが後で動いても追従しない
  （[resolve.go](https://github.com/knative/serving/blob/main/pkg/reconciler/revision/resolve.go)。
  呼び出し側が再解決するかは、このファイルだけでは判断できない）。
- Revision 作成時に digest へ解決し、`Revision.status.imageDigest` に入る。Revision は不変なので、
  `image:latest` に push しても既存 Revision は変わらず、新しい Revision を作るまで反映されない
  （[#12654](https://github.com/knative/serving/issues/12654)、[#12475](https://github.com/knative/serving/issues/12475)）。
  `imagePullPolicy: Always` でも解決済み digest が使われるため効かない。
- `registries-skipping-tag-resolving`（`config-deployment`）で解決を省くレジストリを指定できるが、
  追従させる機能ではなく、解決自体をやめる設定。image に digest を直接書いた場合も解決は省かれる。
- 公式ドキュメントに「追従しない」と明記したページは見つからなかった（knative.dev は取得できなかった）が、
  実機の結果と一致するため、仕様として扱ってよい。

## 2. Knative: Revision を消すとどうなるか

結論: **最新の Revision を消すと、Configuration が新しい Revision を作り直す**（ソースから。実機では未確認）。
ただし、Revision を消す方式は採らない。

- Configuration の reconciler は、`LatestCreatedRevisionName` が指す Revision が見つからない（NotFound）と、
  現在の generation で新しい Revision を作る（[configuration.go](https://github.com/knative/serving/blob/main/pkg/reconciler/configuration/configuration.go)）。
  名前は generation から決まる。`MakeRevision` の中身は確認していない。
- 新しく作られる Revision は、作成時にタグを digest へ解決し直すので、理屈の上では `latest` を取り直す手段になる。
- `LatestReadyRevisionName` は、ready な Revision が見つからない間、明示的には消されない。
- 採らない理由: Revision を消すと、その Revision に流れていたトラフィックが断になりうる（Route が消えた Revision を指す）。
  GKE の Knative のドキュメントには、「トラフィックを受けられる Revision、唯一の Revision、最新の Revision は削除できない」とある
  （[Managing revisions](https://docs.cloud.google.com/kubernetes-engine/enterprise/knative-serving/docs/managing/revisions)。
  マネージド版の制約で、アップストリームでも同じかは未確認）。
- 代わりに、ksvc の `spec.template` に annotation を足して、新しい Revision を作る方式（アクション `new-revision`）を使う。
  こちらは Revision を消さず、Revision の履歴も残る。

## 3. レジストリの rate limit と、このツールの関わり

結論: **このツールの digest の問い合わせは rate limit に数えられない。** 組み込みの制御は入れない。

- Docker Hub では、pull は「`/v2/*/manifests/*` への GET を最大 2 回」と数えられ、**HEAD は数えられない**
  （[Docker Hub rate limit](https://docs.docker.com/docker-hub/download-rate-limit/)）。
  このツールのリモートの digest 取得は `remote.Head`（HEAD）なので、消費しない。
- 数えられるのは、kubelet が行う pull（GET）。ノードごとに、変化したイメージ 1 つにつき pull される。
  対象の IP（匿名）またはアカウント（認証済み）に対する上限で、このツール側の制御で変わる部分は小さい。
- ただし、第一段階の pull 用 Pod は、**ワークロードが動いていないノードも含めて**、Ready な全ノードで pull する。
  ワークロードを動かすための pull より多くなりうる。必要になったら、次を検討する（現状は未実装）:
  pull 用 Pod の同時実行数の制限、ワークロードが動いているノードだけを対象にする。
- 認証: リモートの digest 取得は、既定のキーチェーン（`~/.docker/config.json` など）を使う。
  kubelet の pull は、`--pull-secret`（imagePullSecret）を使う。

## 4. Knative: テンプレートの annotation を変えると、新しい Revision ができるか

結論: **できる**（ソースの読みと、Kubernetes の仕様からの推論。実機では未確認）。`rollout` の Knative 向けの動作は、これに頼っている。

読んだ順に、根拠を並べる。

1. **Service は、Configuration に、テンプレートごとそのまま渡す。**
   Service の reconciler は、`Spec: service.Spec.ConfigurationSpec` と、Configuration の spec を丸ごと代入する
   （[service/resources/configuration.go](https://github.com/knative/serving/blob/main/pkg/reconciler/service/resources/configuration.go)）。
   `spec.template` は、その中に入っているので、テンプレートの metadata（annotation）も一緒に Configuration に入る。
2. **Configuration の CRD は、status サブリソースを持つ。**
   `subresources: status: {}`（[configuration.yaml](https://github.com/knative/serving/blob/main/config/core/300-resources/configuration.yaml)）。
   status サブリソースを持つ CRD では、`metadata.generation` が、spec が変わるたびに増える（Kubernetes の仕様）。
   `spec.template.metadata.annotations` は spec の中なので、これを変えると generation が増える。
   （これは Knative のコードではなく、Kubernetes の仕様からの推論。）
3. **Revision は、generation ごとに 1 つ作られる。**
   Configuration の reconciler は、最新の generation の Revision が見つからなければ、新しく作る
   （[configuration.go](https://github.com/knative/serving/blob/main/pkg/reconciler/configuration/configuration.go)。2 章の調査でも読んだ）。
   Revision の名前は `<Configuration の名前>-<generation を 5 桁で>`、generation のラベルも付く。
   Revision は、テンプレートの `ObjectMeta` と `Spec` を、そのまま写して作られる
   （[configuration/resources/revision.go](https://github.com/knative/serving/blob/main/pkg/reconciler/configuration/resources/revision.go)）。
   テンプレートの annotation も、Revision に引き継がれる。
4. **新しい Revision は、作成時に、タグを digest へ解決し直す**（1 章の調査のとおり。リモートに問い合わせる）。

つまり、`spec.template.metadata.annotations` に、毎回違う値の annotation を足せば、generation が増え、新しい Revision ができて、タグが解決し直される。

- 確認できていない点: 「テンプレートの annotation の変更だけで、新しい Revision ができる」と書いた Knative の文書や、実際のクラスタでの結果は見つかっていない
  （検索でも、直接の記述は見つからなかった）。上の 4 つの根拠をつなげた推論。
  実機で確かめる（e2e には、Knative の確認を入れていない）。
- 注意: `kubectl apply`（クライアント側の apply）で annotation を足すと、`last-applied-configuration` との衝突が起きうる
  （[knative/client#964](https://github.com/knative/client/pull/964)）。このツールは `patch`（JSON merge patch）で足すので、影響しない見込み。
