# 設計メモ（詳細）

README の概要では触れていない、詳細寄りの設計と、**なぜそうしているか**をまとめる。
各項目に状態を付ける: **[実装済み]** / **[未実装]**（方針は決定済み）/ **[未確認]**（実機で確かめていない前提）。

## 1. 前提とスコープ

- **開発環境で、雑にデプロイするケース**を想定している。厳密な運用（本番・ステージングの変更管理）は目的にしない。
- 関心は `latest` などの**フローティングタグ**の更新だけ。固定タグ（`v1.2.3`）や digest 指定は、使われるときに Kubernetes が
  勝手に pull するだけなので、扱わない。
- **ユーザーの管轄のもの（マニフェストの内容）には、なるべく触らない。**
  この方針が、以降の多くの判断（spec の image を書き換えない、アクションは最小の操作にする、など）の根拠になっている。
- 手段は「リモートを正にローカルを同期する」「ローカルを正にリソースを同期する」の 2 段階に分ける（2 章）。

## 2. 全体のモデル: 2 つの同期

```
リモートレジストリ  ──(第一段階)──▶  ローカルキャッシュ  ──(第二段階)──▶  各リソース
   正本                              ノードのイメージストア               Pod / Revision
```

| | 第一段階 | 第二段階 |
| --- | --- | --- |
| 正 | リモートレジストリ | ローカルキャッシュ |
| 同期先 | ローカルキャッシュ | Kubernetes のリソース |
| 比べる値 | リモートのタグの digest と、ローカルの同じタグが指す digest | ローカルのタグが指す digest と、実行中の Pod / Revision の digest |
| 操作 | pull | アクション（組み込みは再デプロイにつながるもの） |

### なぜ 2 段階に分けるか

- **pull するのは kubelet**で、このツールはイメージ本体を落とさない（リモートへは digest の問い合わせだけ）。
  「ローカルを最新にする」と「リソースに取り直させる」は、別の操作で、失敗のしかたも違う。分けておくと、片方だけ動かせる。
- 第一段階はワークロードに一切触れない。**読み取りと pull だけ**なので、Argo CD などとも衝突しない。
  危険度の高い第二段階（アクションの実行）は、後から足せる独立した層にしておく。
- 第二段階がリモートを見ないのは、第一段階でローカルが最新になっているという前提に立つため。
  この前提のおかげで、第二段階の比較は「ローカルの中の値」と「動いているものの値」だけで閉じる。

### 「ベストエフォート」の意味 [第二段階]

第二段階は、アクション（組み込みは再デプロイにつながるもの）で**pull が起こるきっかけを作る**だけで、各リソースがどのイメージで起動するかは保証しない。
起動するイメージは、Kubernetes の pull の仕組み（`imagePullPolicy` など）の結果に任せる。
再デプロイの間などにリモートがさらに進めば、ローカルより新しいイメージで起動することもあるが、許容する。

## 3. `latest` の指す先は 3 か所にある

| 場所 | 持っているもの | 備考 |
| --- | --- | --- |
| リモートレジストリ | タグ → digest の対応（正本） | |
| 各ノードのイメージストア | pull したイメージ。タグの付け替えもここ | ノードごとに別。古い digest が残りうる。`Node.status.images` で読める（kubelet 既定で 50 件まで） |
| Kubernetes の API オブジェクト | spec には `image: x:latest` の文字列だけ | 例外: Pod の `status.containerStatuses[].imageID`、Revision の `status.containerStatuses[].imageDigest` |

Knative は Revision の作成時に、タグを digest に解決して固定する。Revision は不変なので、`latest` に push しても
既存の Revision は変わらない（実機で確認済み。[research.md](./research.md)）。

## 4. ローカルキャッシュ（`Local`）

### 何か [実装済み]

Kubernetes が pull して、各ノードのイメージストア（containerd など）に保持しているイメージ。
Harbor のような**レジストリを建てるものではない**。`internal/collector` の `Local` interface の裏にあり、
既定の実装 `internal/local/node.go` は `Node.status.images` を読む。

### なぜ digest を別に記録しないか

最初は「前回の digest を ConfigMap などに保存して比較する」案だったが、やめた。

- digest はリモートが持っている情報の写しで、別に持つと二重管理になる。
- 実際にイメージを持っているのはノード側で、状態はノード自身が持っている。「何を取り込んだか」を別に覚える必要がない。
- 副産物として、`once`（1 回で終了する形）でも、プロセスをまたいだ記憶なしに動く。

### なぜ「タグが指す digest」で比べるか

`Node.status.images` の 1 エントリは、複数の名前を持つ（例: `repo:latest` と `repo@sha256:X`）。
「そのリポジトリの digest がノードにあるか」で判定すると、次の状態を見逃す。

- Knative が `repo@sha256:B` を digest 指定で pull している（ノードに `B` はある）。
- しかしノード上の `repo:latest` は、まだ旧い `A` を指している。

この状態で、あとから `imagePullPolicy: IfNotPresent` の Pod が `latest` で起動すると、旧い `A` を使う。
そのため、**同じエントリにタグ名が載っている digest だけ**を、そのタグが指す digest として扱う（`TagDigests`）。
digest だけでタグが付いていないものは見ない（使われるときに勝手に pull されるため）。

比較の結果:

| ローカルの TagDigests | 判定 |
| --- | --- |
| リモートの digest ひとつだけ | `unchanged` |
| 空（そのタグを持つノードが無い） | `new`（`--on-new` に従う） |
| それ以外（別の digest、ノード間で食い違い含む） | `changed` → pull で同期 |

### `--on-new`

ローカルがそのタグを持っていない（初見の）イメージの扱い。

- `register`（既定）: pull する。ローカルキャッシュを温めるだけで、ワークロードには触れないので副作用が小さい。
- `ignore`: 何もしない。

初見の扱いと、差分があったときの扱いを分けたのは、「何も無いのは、まだ使われていないだけ」という場合があるため。

### ローカルの種類を選べる形 [実装済み（node のみ）]

`--local` で選べる口がある。現状は `node` のみ。任意のレジストリを同期先にする形（`registry`）は、
`Local` の実装を 1 つ足せば済むように作ってある（[未実装]）。
その場合、利用側（Pod の image）の参照先もそのレジストリに向ける必要があり、第二段階の話と重なる。

### 読み取りのキャッシュ [未実装]

今は呼ぶたびに Node を取得し直す（再スキャン）。結果を保持して高速化する形は、`Local` を包む形で後から付け替えられる。
現状は再スキャンのみ。

## 5. pull の仕組み [実装済み・実機は未確認]

### 方式

kubelet に pull させるため、Ready な各ノードに、そのノード固定（`spec.nodeName`）の短命な Pod を作る。

- **コマンドは存在しないもの**にしてある。コンテナは起動せず、pull だけが起こる。
- コンテナ状態に `imageID` が入ったら pull 完了とみなし、Pod を削除する。`ErrImagePull` などは失敗として返す。
- ノードごとに並行に処理し、`--pull-timeout`（既定 2 分）で打ち切る。

### なぜこの形か

- **kubelet 経由でないと、ノードのイメージストアには入らない。** コレクター自身はノードに書けない。
- **DaemonSet にしなかった**: image を変えるたびに作り直しが要り、完了の判定も難しい。
- **`nodeName` 固定**: スケジューラを通さず、確実にそのノードで pull させる（Ready でないノードは除く）。
- **Pod 名**に、タグ参照・digest・ノード名のハッシュを使う。digest が変わるたびに別の Pod になり、前回の残りと衝突しない。

### なぜタグ指定で pull するか

ワークロードと同じ `repo:latest` で pull する。digest 指定（`repo@sha256:…`）で pull すると、イメージ本体はノードに入るが、
**ノード上のタグ（`latest`）は旧いまま**で、4 章の見逃しと同じ問題が起きる。タグで pull すれば、ノードのタグが付け替わり、
あとで `IfNotPresent` の Pod が作られても新しいイメージで起動する。

代償として、検知してから pull するまでの間にタグがさらに進むと、検知した digest より新しいものが来ることがある。許容する。

### 認証と名前空間

- `--pull-namespace`（既定 `default`）に Pod を作る。非公開レジストリは `--pull-secret`（同じ名前空間の imagePullSecret）を使う。
- リモートの digest 取得の認証は、既定のキーチェーン（`~/.docker/config.json` など）を使う。手元から動かす前提。

### 必要な権限

Node の list、`--pull-namespace` での Pod の create / get / delete。対象リソースの list（5 章）。
RBAC のマニフェストは [未実装]。

## 6. 対象の収集（`Source`） [実装済み]

- **annotation による opt-in**: `floatingtag-pinner/enabled: "true"` が付いたリソースだけが対象。付けない限り何もしない。
- 対象の種類: Deployment / StatefulSet / DaemonSet / Knative Service。**動的クライアント（unstructured）**で読むので、
  Knative の Go パッケージに依存しない。CRD が入っていないクラスタ（NotFound）は、黙って飛ばす。
- コンテナと initContainer の image を取り出し、**重複を除き**、参照元（`ns/Kind/name`）を束ねる。
  同じイメージを複数のリソースが使っていても、リモートへの問い合わせと pull は 1 回で済む。
- **フローティングタグだけを追う**: `--floating-tags`（既定 `latest`、タグ無しは `latest` 扱い）。
  「フローティングか固定か」はプログラムから判別できないので、追うタグを明示する形にした。
  `main` や `dev` を追う場合は、`--floating-tags=latest,dev` のように足す。空にすると全タグを追う。
- `@sha256:` を含む image は除く（第一段階の方針。第二段階の扱いは 8 章）。

## 7. 実行形態と責務の分離 [実装済み]

```
cmd/pinner  ──▶  internal/collector（収集と比較のロジック）
                    ├─ Source   : 対象イメージを集める   (internal/source)
                    ├─ Registry : リモートの digest      (internal/registry)
                    └─ Local    : ローカルの digest と pull (internal/local)
```

- `collector` は 3 つの interface だけに依存する。実行形態（1 回実行、常駐、将来の operator）には依存しない。
  どれで動かすかは `cmd` の側で決める。
- コマンドは**段階ごとに分ける**（`pull` = 第一段階、`trigger` = 第二段階、`sync` = 続けて両方。`sync` の名前は仮）。
  - 第一段階は読み取りと pull だけ、第二段階はアクションの実行（再デプロイなど）を伴う。**リスクも必要な権限も違う**ので、RBAC を分けられ、
    第二段階を意図せず動かすこともない（`pull` だけでは何も変更しない）。
  - 動かす間隔も別にできる（第一段階は頻繁に、第二段階はゆっくり、など）。
  - 続けて動かしたいときのために、`sync` で両方をこの順に行う。
  - 現状は `pull` のみ実装。`trigger` と `sync` は第二段階と一緒に実装する。
- 実行形態は、どのコマンドでも既定が**1 回実行して終了**で、`--daemon`（と `--interval`）で常駐する。
  最初に必要なのは 1 回の実行で、繰り返しはその上に足せば済むため。
- エラー: イメージごとの失敗は結果に載せて続行する。対象の一覧の取得に失敗したときだけ、その回を中断する。
  1 回実行では、どれか 1 つでも失敗があれば終了コードは非 0。
- リモートの digest は `remote.Head` で取る（イメージ本体は落とさない）。

## 8. 第二段階の設計 [検知と `info` / `none` は実装済み。ほかのアクションは未実装]

### 比較

ローカルのタグが指す digest と、**実行中**のものの digest を、Pod が動くノードごとに比べる。

- Deployment / StatefulSet / DaemonSet: Pod の `imageID`。タグは Pod の image 欄で分かる。
- Knative: Revision の `imageDigest`。Pod の image は digest で固定されていて、タグは Revision / ksvc の spec にある。

Pod から取るのは「タグ」と「digest」の両方。タグはどのローカルのエントリと比べるかの鍵で、比べるのは digest 同士。

### 実装の状況

- 検知（`internal/trigger` の `Detector`）: 実装済み。リソースの収集（`source.Kube.Resources`）、
  ノードごとのローカルのタグの digest（`local.Node.NodeTagDigests`）、実行中の digest（`source.KubeRunning`）を比べる。
  Pod はそのノードのタグと、Revision はローカル全体のタグと比べる。結果は `match` / `mismatch` / `not-pulled` / `not-running` / `error`。
- アクター（`Actor`）とアクション: `info`（既定）と `none` だけ実装済み。アクション名が未実装のときは、エラーにする。
- `trigger --dry-run`: 実装済み。どのアクションも `info` として扱う。
- 未実装: `delete-pods`、`restart`、`new-revision`、`sync`（pull → trigger）。
- 実機では未確認。

### アクター（Actor）とアクション（Action）

第二段階の核は、**「リソースがローカルキャッシュと違うことの検知」**。違う digest が古いのか新しいのかは分からない
（ローカルを正にするのは方針で、新旧の事実ではない）。何をするかは**アクション**として切り離し、それを実行する側を**アクター**と呼ぶ（Actor が Action を実行する）。
コマンド名も `redeploy` ではなく、中立な `trigger`（きっかけを作るだけで、結果は保証しない）にしてある。再デプロイにつながる操作が組み込まれているのは、
このプロジェクトに相性がよいためで、アクションの種類を再デプロイに限る意図はない。

- アクションは、リソースの annotation で指定する: `floatingtag-pinner/action: <名前>`。
- **未指定は `info`（差分をログに出すだけ）**。ユーザーの管轄のものには、頼まれない限り触らない。
  リソースに手を入れずに、検知できているかを確かめる手段にもなる。`none` は、検知した結果にも何もしない（ログも出さない）。
- `trigger --dry-run`（予定）は、どのアクションも `info` として扱うスイッチ。`info` はリソース単位、`--dry-run` は全体の指定。
- アクションは、対象の種類ごとに適用できるものが決まっている。適用できない組み合わせは、エラーにする。
- 実装は、`Action`（種類に適用できるかの判定と、実行）の interface の裏に置き、`Actor` が annotation で指定された `Action` を選んで実行する。
  アクションを足しても、検知は変わらない。
  ログ以外（Event の作成、通知など）も、後から足せる。

組み込みの予定（いずれも、spec の image は書き換えない。**最小の操作で、pull が起こるきっかけを作る**）:

| アクション | 対象 | 操作 | spec への影響 |
| --- | --- | --- | --- |
| `none` | すべて | 何もしない | 無し |
| `info`（既定） | すべて | 差分があることをログに出す | 無し |
| `delete-pods` | Deployment / StatefulSet / DaemonSet | ローカルと違う Pod を削除する（ReplicaSet 等が作り直す） | 無し |
| `restart` | Deployment / StatefulSet / DaemonSet | Pod テンプレートに `restartedAt` の annotation を付けて、段階的に入れ替える | annotation を 1 つ足す |
| `new-revision` | Knative Service | ksvc の `spec.template` に annotation を足し、新しい Revision を作る | annotation を 1 つ足す |

Knative で Pod を消しても意味がないのは、Revision の Pod が解決済みの digest で固定されているため（同じ旧いイメージで作り直される）。
新しい Revision を作ると、そのときタグが解決し直される。このとき解決するのは、ローカルではなくリモートへの問い合わせ。

### 採らなかった案: spec の image に digest を書く（`repo:latest@sha256:…`）

ユーザーの管轄のものに触り、次のような問題が出るため採らない。

- マニフェストを push / `kubectl apply` するたびに `latest` に戻り、ローリングが走り、ツールがまた書き込む（行き来）。
- Argo CD / Flux の selfHeal に戻される。Helm の upgrade に上書きされる。server-side apply の field manager が衝突する。
- git とクラスタが食い違い、git が正本でなくなる。spec は `latest` と読めるのに、実際は固定された digest で動く。
- ツールが止まると、固定された古い digest のまま残り、新しい Pod もそれを使う。
- Kyverno / OPA のポリシー（タグ必須、署名検証）に弾かれうる。

（digest とタグを併記した `repo:latest@sha256:…` では、kubelet は digest を使いタグは無視する、と理解している [未確認]。）

### Argo CD との関係 [未確認]

image を書き換えないので衝突は大きく減る。確認が要るのは、テンプレートへの annotation（`restartedAt`、ksvc の template annotation）を、
selfHeal が差分として戻さないか。戻すなら `ignoreDifferences` を使う。Pod の削除は spec を変えないので影響しない見込み。

## 9. 検討中のこと

- 結果の出力先の追加（Event、メトリクス。現状はログのみ）。
- クラスタの中で動かす構成（CronJob / Deployment、RBAC、operator 化）は、いまは考えない。手元から `kubeconfig` で動かす前提。
- pull 用 Pod の同時実行数の制限や、ワークロードが動いているノードだけを対象にすること（[research.md](./research.md) の rate limit の節）。

## 10. 実機で確認が必要な前提 [未確認]

実機では、第一段階のコード全体が未確認。特に次の点を見る。

1. pull 用 Pod（存在しないコマンド）で、使っているランタイムが pull だけ行い、`imageID` を返すか。
2. `Node.status.images` の名前の並び（`repo:latest` と `repo@sha256:…` が同じエントリに載るか）。
3. マルチアーキ: リモートで取る digest はタグ全体（index）の digest。ノードの名前や Pod の `imageID` も、同じ index の digest か。
   個別（platform）の digest が載るランタイムだと、新しいのに「不一致」と判定される。その場合は、ノードの OS とアーキに対応する
   個別の digest を引いて比べる方式に切り替える。アーキごとの digest は取れるが、今は使っていない。
   タグ全体の digest は、どのアーキが変わっても変わるので、「タグが動いたか」の検知は、これで足りる。
4. Knative: ksvc の `spec.template` への annotation で、新しい Revision ができ、タグが解決し直されること。
5. 非公開レジストリでの認証（digest 取得と pull）。rate limit は [research.md](./research.md) のとおり、digest の取得は数えられない。
6. `Node.status.images` が 1 ノード 50 件まで（kubelet の既定）のため、全ノードの和集合で見ても、載らないことがある。

## 用語

| 用語 | 意味 |
| --- | --- |
| フローティングタグ | `latest` など、指す先が動くタグ。追う対象 |
| ローカルキャッシュ | 各ノードのイメージストア（4 章） |
| index digest | マルチアーキのタグ全体が指す digest。platform digest はアーキごとの個別のもの |
| 正 | 比較の基準。第一段階はリモート、第二段階はローカル |
| アクター / アクション | アクター（Actor）は、検知した差分に対して、指定されたアクション（Action）を実行する側。アクションは「何をするか」（`info`、`none`、`delete-pods` など） |
