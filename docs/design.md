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
- 第一段階はワークロードに一切触れない。**読み取りと pull だけ**。
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
| リモートの digest ひとつだけ | `match` |
| 空（そのタグを持つノードが無い） | `not-pulled`（`--on-new` に従う） |
| それ以外（別の digest、ノード間で食い違い含む） | `mismatch` → pull で同期 |

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

## 5. pull の仕組み [実装済み・実機で確認済み]

他の方式（ノードの常駐エージェントが直接 pull する、DaemonSet で pull するなど）との比較は、[research.md](./research.md) の 5 章を参照。

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

### なぜ `imagePullPolicy: Always` か（実機で見つかった不具合）

pull 用 Pod は、`Always` にしている。最初は `IfNotPresent` にしていたが、**ノードに、そのタグがすでにあって、古い digest を指しているとき**
（= `mismatch` のとき）に、何も pull されなかった。`IfNotPresent` は、タグの名前がノードにあれば「ある」と判断して、レジストリに問い合わせない。
そのため、pull 用 Pod は、古いイメージのまま起動して、古い digest（`imageID`）を報告した。リモートが `07e709f6…` に動いたのに、
pull のあとも `local=[bf16c8e0…]` のままで、`pull mismatch` が繰り返された。
ノードにタグが無いとき（`not-pulled`）は、`IfNotPresent` でも pull されるので、最初の確認では見つからなかった。
`Always` は、毎回レジストリに問い合わせ（イメージ本体は、digest が違うときだけ取る）、違えば pull して、ノードのタグを付け替える。

pull のあとの状態を、pull 用 Pod が報告した digest から求める方式（下）は、この不具合でも役に立った: `outcome=pulled` と言いながら、
続く `via=pulling` の行が `mismatch` のままで、「pull したが、揃っていない」と正しく出た。

**pull 用 Pod の名前は、毎回違うものにする。** 最初は、（タグ参照、digest、ノード名）のハッシュで、同じ状況なら同じ名前だった。
直前の実行の Pod が、まだ消えかけの状態で残っていると、`Create` が `AlreadyExists` になり、**古い Pod の状態（古い digest）を、今回の pull の結果として読んでしまう**
おそれがある。そこで、名前に、実行ごとの値（時刻）を混ぜて、衝突させないようにした。

### デバッグと、どのビルドか

- `--debug`（= `--log-level=debug`）で、ノードごとの pull の結果（Pod の `containerStatuses` の中身を含む）、ノードが報告するタグの digest、
  動いている Pod / Revision が報告する `imageID` / `imageDigest` を、ログに出す。通常のログは、結果の状態だけで、「なぜそうなったか」は見えないため。
- `pinner version` で、どのビルドか（コミット、Go のバージョン）が分かる。CI は、`-X main.commit=<sha>` でコミットを埋め込む
  （Release のバイナリ、コンテナイメージ）。手元で git のチェックアウトからビルドしたときは、ビルド情報のリビジョンを使う（変更があれば `+modified`）。
  実機で、修正前のバイナリを使っていたのか、修正後でも直らないのかが、区別できなかったことが、きっかけ。

### なぜタグ指定で pull するか

ワークロードと同じ `repo:latest` で pull する。digest 指定（`repo@sha256:…`）で pull すると、イメージ本体はノードに入るが、
**ノード上のタグ（`latest`）は旧いまま**で、4 章の見逃しと同じ問題が起きる。タグで pull すれば、ノードのタグが付け替わり、
あとで `IfNotPresent` の Pod が作られても新しいイメージで起動する。

代償として、検知してから pull するまでの間にタグがさらに進むと、検知した digest より新しいものが来ることがある。許容する。

### 認証と名前空間

- `--pull-namespace`（既定 `default`）に Pod を作る。pull の認証は、Pod の pull の仕組みに従う: 基本はノード側
  （kubelet の credential provider、kubelet が読むノード上の `config.json`、containerd の認証設定）。`imagePullSecrets` は扱わない（アプリのユーザーが、自分の権限の Secret で pull させるための仕組みで、運用する側のツールには合わない）。
- リモートの digest 取得の認証（`pinner` 自身がレジストリに接続する）は、pull の認証（kubelet が接続する）とは**別**。
  `--credential <ファイル>`（`pull` のフラグ。docker の `config.json` と同じ形式。`registry.FileKeychain`）で渡す。そのファイルにないレジストリは、匿名。
  省略すると、既定のキーチェーン。ノードの `config.json` を指定すれば、認証の元を 1 つにできる（`pinner` をそのノード上で動かす場合）。

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

## 収集（`collect`）: pull と trigger の土台

`pull` も `trigger` も、まず「どのリソースの、どのイメージを対象にするか」を決める。その部分を、コマンド `pinner collect` として、
単独で見られるようにした（読み取りのみ。`internal/source` の `Kube.Resources`）。

```
collect ──▶ pull    （対象のイメージを、リモートとローカルで比べて、pull する）
        └─▶ trigger （対象のリソースを、ローカルと実行中で比べて、アクションを依頼する）
```

- 対象の選び方（`--annotation`、`--floating-tags`、`--namespace`）は、3 つのコマンドで共通。`collect` で見えたものが、`pull` と `trigger` の対象になる。
  フラグも、コード（`selection`）も共通。「なぜ、このイメージが対象にならないのか」は、`collect` で理由まで見られる。
- 対象外のイメージは、理由と一緒に持つ（`Resource.Skipped`）: digest 指定（`pinned by digest`）、フローティングタグでない（`tag is not floating`）。
  対象のイメージが 1 つもなくても、annotation が付いていれば、リソースは一覧に出る（`pull` と `trigger` は、対象のイメージだけを見る）。
- 出力は、ログではなくデータなので、標準出力に出す（表、または `-o json`）。ログ（`--log-level`、`--log-states`）の対象ではない。
- 実装の順序は逆になったが、本来は、最初に作るべき部分だった。

## 8. 第二段階の設計 [検知、`none`、`rollout`、`delete-pods` は実装済み。`sync` は未実装]

### 比較

ローカルのタグが指す digest と、**実行中**のものの digest を、Pod が動くノードごとに比べる。

- Deployment / StatefulSet / DaemonSet: Pod の `imageID`。タグは Pod の image 欄で分かる。
- Knative: Revision の `imageDigest`。Pod の image は digest で固定されていて、タグは Revision / ksvc の spec にある。

### 実行中の Pod

Pod が「そのイメージで動いている」と数えるのは、次をすべて満たすとき（`source.inUse`、テストは `TestRunningPodStates`）。

- 削除中でない（`deletionTimestamp` が無い）。ロールアウトが終わる間、古い Pod は終了処理中のまま一覧に残り、古い digest を持つ。これを数えると、`mismatch` の誤報になる（e2e で見つかった）。
- 終わっていない（`status.phase` が `Succeeded` / `Failed` でない）。Job が残した Pod や、退避（evict）された Pod は、もう動いていない。
- そのイメージの `containerStatuses` に、repo の digest つきの `imageID` がある。まだ pull されていない（`imageID` が空）、または digest が無い（イメージ ID だけ）ものは、比べる相手がないので数えない。

`pinner` が Pod から読むのは、`deletionTimestamp`、`status.phase`、`spec.nodeName`、`containerStatuses[].image` / `imageID` だけ。
状態の洗い出しは、この 5 項目の組み合わせに限る。e2e などで新しい状態が見つかったら、この定義と `TestRunningPodStates` に足す。

Pod から取るのは「タグ」と「digest」の両方。タグはどのローカルのエントリと比べるかの鍵で、比べるのは digest 同士。

### 実装の状況

- 検知（`internal/trigger` の `Detector`）: 実装済み。リソースの収集（`source.Kube.Resources`）、
  ノードごとのローカルのタグの digest（`local.Node.NodeTagDigests`）、実行中の digest（`source.KubeRunning`）を比べる。
  Pod はそのノードのタグと、Revision はローカル全体のタグと比べる。結果は `match` / `mismatch` / `not-pulled` / `not-running` / `error`。
- アクター（`Actor`）とアクション: `none`（既定）、`rollout`、`delete-pods` が実装済み（`internal/action`）。アクション名が未実装のときは、エラーにする。
  同じ状態への繰り返しの回避と、`--max-actions` も `Actor` にある（下の「アクター」の節）。
- `trigger --dry-run`: 実装済み。アクションを実行せず、どのアクションが実行されるはずだったかをログに出す。
- 未実装: `sync`（pull → trigger）。
- 実機では未確認。

### ログ（状態とログレベル）

`pull` も `trigger` も、見つけたことを、同じ名前の**状態**で報告する。比べる相手だけが違う。

| 状態 | 第一段階（リモート ⇔ ローカル） | 第二段階（ローカル ⇔ 実行中） | ログレベル |
| --- | --- | --- | --- |
| `match` | 一致 | 一致 | INFO |
| `mismatch` | 違う（新旧は分からない） | 違う（新旧は分からない） | WARN |
| `not-pulled` | ローカルがタグを持っていない | 同じ | WARN |
| `not-running` | （なし） | 比べる相手が無い | INFO |
| `error` | 失敗 | 失敗 | ERROR |

- ログレベルは、状態ごとに**固定**で、第一段階と第二段階で同じ考え方（何もすることがない = INFO、違いがある = WARN、失敗 = ERROR）。
  状態の名前を共有したので、`--log-level` と `--log-states` の意味が、両方で同じになる。
- 第一段階の旧名（`unchanged` / `changed` / `new` / `ignored`）は、`match` / `mismatch` / `not-pulled` に統一した。
  `new` と `not-pulled` は同じ状況（ローカルがそのタグを持っていない）。`pull` が違いに対してしたこと（pull した、しなかった、
  `--dry-run` で pull が必要だった）は、状態ではなく、ログの `outcome` に出す。
- ログの行は、**段階の接頭辞**（`pull` / `trigger`）で始める（`pull mismatch`、`pull match`、`trigger mismatch`）。
  単体実行でも、`sync`（続けて実行）でも、形式が揃う。接頭辞は、`msg` の先頭に付ける（`stage` の項目にはしない。重複するため）。
  状態でない行（`trigger action requested`、`collect failed` など）にも付ける。
- **「イベント」ではなく「状態」と呼ぶ。** `match` などは、比べた結果の状態で、第一段階はそれが変化し、第二段階は 1 回だけ観測する。
- **第一段階は、状態が変化する。** `pull` が pull を自分で行うので、そのあとの状態を報告する（`mismatch` → `match`、`via=pulling`）。
  「pull の依頼が成功した」ことと、「ノードのタグがリモートの digest を指すようになった」ことは別で、
  pull の間にリモートがさらに進んで、別の digest を取ってきた（タグ指定で pull するため）ことがありうる。
  そこで、pull のあとの状態は、**pull 用 Pod が報告した digest**（`containerStatuses[].imageID`）から求める:
  すべてのノードがリモートの digest を報告したら `match`、別の digest があれば `mismatch`、何も報告されなければ `not-pulled`。
  pull 用 Pod の `imageID` は、pull が済んだ時点で分かる、正確な情報。
- **`Node.status.images` を読み直して確かめない理由（実機で分かったこと）。** 最初は、pull のあとに `Node.status.images` を読み直し、
  最大 30 秒、2 秒おきに繰り返す、という設計だった（kubelet が 10 秒程度で更新すると想定していた）。実機では、pull は成功していたのに、
  30 秒後も、タグが見えなかった。kubelet が `Node.status.images` に載せる一覧は、kubelet の内部のキャッシュから作られ、
  そのキャッシュは、数分ごとにしか更新されない、と考えている（kubelet のコードの理解。実機で、数分後に載ることを確かめる）。
  読み直しにすると、うまくいった pull を、失敗と判断する。
- **この遅れの影響が残るところ**: ローカルの状態を読むのは、`Node.status.images` から（`TagDigests`、`NodeTagDigests`）。
  `pull` の直後（数分以内）に、もう一度 `pull` や `trigger` を実行すると、まだ `not-pulled` / `mismatch` と見えることがある。
  pull は、すでにあるイメージには何もしない（安い）ので、害は小さい。しばらく待てば、`match` になる。
- **第二段階は、投げっぱなし。** 最初の状態を報告し、アクションを依頼する（`trigger action requested`）だけで、結果は追わない。
  アクションの結果は Kubernetes の pull の仕組みが決めるので、確かめようがない（ベストエフォート）。次の実行で、新しい状態として分かる。
- `--log-level`（下限）と `--log-states`（出す状態）で、実行時に絞る。そのコマンドが出さない状態の指定は、起動時にエラー。
- 実装は `internal/report` の 1 か所（状態 → ログレベル、絞り込み）。コマンドは、それを使って報告する。

### アクター（Actor）とアクション（Action）

第二段階の核は、**「リソースがローカルキャッシュと違うことの検知」**。違う digest が古いのか新しいのかは分からない
（ローカルを正にするのは方針で、新旧の事実ではない）。何をするかは**アクション**として切り離し、それを実行する側を**アクター**と呼ぶ（Actor が Action を実行する）。
コマンド名も `redeploy` ではなく、中立な `trigger`（きっかけを作るだけで、結果は保証しない）にしてある。再デプロイにつながる操作が組み込まれているのは、
このプロジェクトに相性がよいためで、アクションの種類を再デプロイに限る意図はない。

- アクションは、**状態**（`trigger` のステータス）ごとに、リソースの annotation で指定する。
  - `floatingtag-pinner/on-mismatch: <名前>`: ローカルと違うとき。**ふつうは、これだけ指定すればよい**。
  - `floatingtag-pinner/on-not-pulled: <名前>`: ローカルキャッシュがタグを持っていないとき（任意）。
  - `floatingtag-pinner/on-error: <名前>`: 比較の途中で失敗したとき（任意）。終了コードは、アクションにかかわらず非 0。
  - `match` と `not-running` には結び付けない（何もしない）。
  - どれも未指定なら `none`（何もしない）。**`on-not-pulled` は `on-mismatch` を引き継がない。** `not-pulled` は比べる相手が無く、
    何を直すのか分からないため。たとえば Knative は、Revision の Pod が digest で pull するのでノードにタグが付かず、
    `not-pulled` のままになりうる。`rollout` を引き継ぐと、`--daemon` の周期ごとに新しい Revision を作り続ける。
    対処は、アクションではなく、第一段階の `pull`（pinner 駆動の pull）。
  - 「誰が pull するか」（第一段階: `pull` を使う＝pinner 駆動、使わず Pod の起動に任せる＝Pod 駆動）と、
    「違いを見つけたとき何をするか」（第二段階: このアクション）は、別の軸。
  - 今後、`not-running` などにも結び付けられるようにする余地はあるが、いまは `mismatch`、`not-pulled`、`error` だけ。
  - Go のエラーにはスタックトレースが付かない。代わりに、各層でエラーに文脈を足している（`local: …`、`running: …` など）。
    スタックトレースが要る場合は、`--debug` のようなフラグで後から足せる。
- **ログ（報告）とアクション（作用）は別のもの。** 見つけたことは、アクションと関係なく、常にすべて報告する（`internal/report`）。
  出す範囲は、実行時のオプションで絞る（`--log-level`、`--log-states`。次節）。アクションは、リソースに何かをするものだけ。
  最初は `info` というアクションを置いていたが、「報告すること」と「作用すること」が混ざるので、やめた。
- **既定は `none`（何もしない）**。ユーザーの管轄のものには、頼まれない限り触らない。見つけたことはログに出るので、
  リソースに手を入れずに、検知できているかを確かめられる。
- `trigger --dry-run` は、アクションを実行せず、どのアクションが実行されるはずだったかをログに出す。ログ自体は常に出ている。
- アクションは、対象の種類ごとに適用できるものが決まっている。適用できない組み合わせは、エラーにする。
- 実装は、`Action`（種類に適用できるかの判定と、実行）の interface の裏に置き、`Actor` が annotation で指定された `Action` を選んで実行する。
  アクションを足しても、検知は変わらない。
  ログ以外（Event の作成、通知など）も、後から足せる。

組み込み（いずれも、spec の image は書き換えない。**最小の操作で、pull が起こるきっかけを作る**）:

| アクション | 状態 | 対象 | 操作 | spec への影響 |
| --- | --- | --- | --- | --- |
| `none`（既定） | 実装済み | すべて | 何もしない（見つけたことは、ログに出る） | 無し |
| `rollout` | 実装済み・Knative は実機で確認済み（Deployment 等は未確認） | Deployment / StatefulSet / DaemonSet / Knative Service | Pod テンプレートに annotation を足して、新しい世代を出す | annotation を 1 つ足す |
| `delete-pods` | 実装済み・実機は未確認 | Deployment / StatefulSet / DaemonSet | `mismatch` だった Pod だけを削除する（ReplicaSet 等が作り直す） | 無し |

**アクションは「意図」で名付け、種類ごとの違いは、対象の処理に持たせる。**
Deployment 等の「ローリング更新」と、Knative の「新しい Revision」は、どちらもテンプレートの annotation を 1 つ変える同じ操作
（`spec.template.metadata.annotations` を JSON merge patch する）で、対象の種類（API の種類）が違うだけ。
そこで、`restart` と `new-revision` を別のアクションにせず、`rollout`（新しい世代を出す）にまとめた。
`delete-pods` は、Knative には対応しない（`Supports(kind)` が、対応しない組み合わせをエラーにする）。

- Knative で、テンプレートの annotation の変更が新しい Revision を作ることは、ソースの読みで確かめ、**実機でも確認した**（[research.md](./research.md) の 4 章。Revision `00007` の `4a45bcf8…` → `rollout` で `00008` の `bf16c8e0…`）。
- annotation の key は `floatingtag-pinner/restartedAt`（値は UTC の時刻、秒単位）。`kubectl rollout restart` の
  `kubectl.kubernetes.io/restartedAt` とは別にして、kubectl との衝突を避ける。
  同じ resource の 2 つの finding（たとえば 2 つの image）が同じ秒に patch しても、同じ値なので、2 回目は何も変えず、rollout は 1 回で済む。
- field manager は `floatingtag-pinner`。誰が変えたかが、`managedFields` で分かる。
- 実装は `internal/action`。`kind → API resource` の対応は、`source` と共通（`source.ResourceFor`）。

**`delete-pods` の設計**

- 削除するのは、`mismatch` の finding が名指しした Pod だけ（`Finding.Mismatches[].Pod`）。揃っている Pod には触れない。リソース（Deployment 等）には触れない。
- **UID を条件に削除する**: 検知してから削除するまでの間に、その Pod がすでに入れ替わっていたら（StatefulSet は名前が同じ）、新しい Pod を消さない。
  条件に合わない（Conflict）、または、すでに無い（NotFound）は、目的が済んでいるので、エラーにしない。そのため、`trigger.Running` が Pod の UID を持つ。
- Knative には対応しない。Revision の Pod は、解決済みの digest で固定されていて、消しても同じものが作り直されるだけ。
- **`mismatch` にだけ結び付けられる**（`StateAware`）。`not-pulled` や `error` では、削除する Pod を名指しできないため、`on-not-pulled=delete-pods` はエラーにする。
- **まとめて削除する**: 違う Pod を一度に消すので、一時的に Pod が足りなくなることがある。Eviction API（PodDisruptionBudget を尊重する）や、
  1 つずつ消す方式は採らなかった。開発環境向けで、単純さを取った。同じ状態は 1 回しか依頼しないので（下の「アクター」）、
  「1 回の実行で 1 つずつ」にすると、残りの Pod が入れ替わらなくなる。段階的に入れ替えたいときは、`rollout`（ローリング更新）を使う。

### アクター（繰り返しの回避と上限）と、ステートストア

`Actor` は、アクションを実行する前に、次の 2 つを見る。

- **同じ状態には 1 回だけ依頼する**（`once-per-state`）: key は（リソース、イメージ、状態、アクション）、記録するのは、そのときのローカルの digest。
  `rollout` したあとも、リソースがしばらく（または、ずっと）ローカルと違うことがある。毎回依頼すると、`--daemon` で、周期ごとに rollout し続ける。
  ローカルの digest が変われば、別の状態なので、また依頼する。`--dry-run` は、数えない。
- **`--max-actions`**: 1 回の実行（pass）で依頼する数の上限（0 = 上限なし）。次の pass では、また 0 から数える。

**ステートストア**（`internal/state`）: 上の「何を依頼したか」を、`Store` の interface の裏に置く。いまは、メモリ上の辞書（`Memory`）だけ。
プロセスが終わると、忘れる（`trigger` を 1 回実行で繰り返すときも、毎回忘れる）。

- 持っているもの（`Record`）: 依頼したときのローカルの digest、依頼した時刻、依頼の失敗の連続回数（`Failures`）、
  依頼したのに同じ状態のままだった回数（`StillDifferent`。アクションが効かなかった印）、最後のエラー。
  ベースライン（比較の基準）ではなく、アクションの履歴。消えても、余分にもう 1 回、依頼するだけで、壊れない。
- **まだ実装していない戦略**（`internal/state` のコメントに書いてある）: `cooldown`（同じリソースへの間隔を空ける）、
  `backoff`（失敗や効かなかったことが続くほど、間隔を伸ばす。更新が、うまくいかないところで消極的になる）、
  `circuit-breaker`（1 回の実行で失敗が閾値を超えたら、止める）。どれも、同じ `Record` を読み書きして、`Actor` の「実行してよいか」で判断する。
- 「効かなかった」の判定: `trigger` は、依頼したあとの結果を追わない（投げっぱなし）が、次の実行で同じリソースをもう一度見る。
  依頼したのに、同じローカルの digest のまま、まだ違えば、効かなかった。いまは、その回数を数えるだけ（`StillDifferent`）。
- 永続化する `Store` の候補: ファイル（JSON。`trigger` を手元や cron から繰り返し起動するときに効く。この規模なら、依存は要らない）、
  ConfigMap（クラスタの中で動かすとき）。リソースの annotation には書かない（ユーザーのリソースに書くことになるため）。
  組み込みのキーバリュー DB（bbolt など）は、複数のプロセスが同時に書く、データが大きい、といったときだけ、価値がある。

Knative は、Revision を作ると、そのときリモートに問い合わせてタグを解決する（ローカルではない）。
そのため、`rollout` で作られた Revision の digest は、ローカルのタグと、通常は一致する（`pull` のあと）。

### 採らなかった案: spec の image に digest を書く（`repo:latest@sha256:…`）

ユーザーの管轄のものに触り、次のような問題が出るため採らない。

- マニフェストを push / `kubectl apply` するたびに `latest` に戻り、ローリングが走り、ツールがまた書き込む（行き来）。
- Argo CD / Flux の selfHeal に戻される。Helm の upgrade に上書きされる。server-side apply の field manager が衝突する。
- git とクラスタが食い違い、git が正本でなくなる。spec は `latest` と読めるのに、実際は固定された digest で動く。
- ツールが止まると、固定された古い digest のまま残り、新しい Pod もそれを使う。
- Kyverno / OPA のポリシー（タグ必須、署名検証）に弾かれうる。

（digest とタグを併記した `repo:latest@sha256:…` では、kubelet は digest を使いタグは無視する、と理解している [未確認]。）

## 9. 検討中のこと

- **`--pull-via cri`（仮。残課題）**: `pinner` をノード上で動かし、containerd に CRI（gRPC のソケット）で直接、pull とローカルの digest の読み取りをさせる。
  いまの方式（`--pull-via kubelet` にあたる。pull 用の Pod を作り、kubelet に任せる）との切り替えを想定する。
  - 利点: 認証の元が 1 つ（`--credential` のファイルを、digest の問い合わせにも、CRI の `PullImage` の認証にも使う）。Pod も `--pull-namespace` も要らない。
    CRI の `ImageStatus` で、ローカルの digest を即時に読める（`Node.status.images` の数分の遅れがなくなる）。
  - 制約: そのノードの上で動かす必要がある（複数ノードなら、ノードごと）。ソケットへの権限が要る。CRI の gRPC クライアントと、テスト用の偽の CRI サーバーの実装が要る。
    そのノードの Pod だけを比べる（ほかのノードの Pod は比べない）扱いが要る。
  - kubelet の部品を流用するのではなく、kubelet と同じ入口（CRI）から、`pinner` が直接 containerd に頼む。認証の処理は `pinner` が持つ（`config.json` を読んで、要求に付ける）。
  - 軸は、`--local`（ローカルとは何か。いまは `node` のみ）とは別で、「ノードのストアにどう触るか」の選択。

- 結果の出力先の追加（Event、メトリクス。現状はログのみ）。
- クラスタの中で動かす構成（CronJob / Deployment、RBAC、operator 化）は、いまは考えない。手元から `kubeconfig` で動かす前提。
- pull 用 Pod の同時実行数の制限や、ワークロードが動いているノードだけを対象にすること（[research.md](./research.md) の rate limit の節）。

## コマンドラインのドキュメント（生成と公開）

- **フラグの定義から生成する**（`pinner docs`）。コマンドの一覧（`cmd/pinner/cli.go` の `commands`）が、使い方の文字列と、`docs/cli/*.md` の
  両方の唯一の元で、フラグ（名前、型、既定値、説明）は、解析に使っている定義（`flag.FlagSet`）から読む。
  手で書いた表と、実際のコードが食い違わない。フラグの説明は、`--help` と同じ（英語）。コマンドごとの説明（日本語）だけ、`commands` に持つ。
- 生成した `docs/cli/*.md` は、**コミットする**（差分が見える）。`TestCLIDocsAreUpToDate` が、生成の結果と一致するかを確かめ、
  フラグを足して、再生成を忘れると、CI が落ちる。`docs` コマンドは、使い方の一覧には出さない。
- cobra などの CLI ライブラリには載せ替えていない（依存が増え、いまの `flag` のコードを書き直すことになるため）。標準の `flag` のまま、
  フラグの定義を読んで Markdown にする、数十行の自作で足りる。
- **公開**（`.github/workflows/docs.yml`）: MkDocs（Material）で、README と `docs/` を静的サイトにして、GitHub Pages に出す。
  README は `docs/` の外にあるので、`scripts/prepare-docs-site.sh` が、`.site-src/`（コミットしない）に、README をホームにした入力を作り、
  サイトで解決できないリンクを、リポジトリの URL に直す。`docs/` 自体は書き換えない。
  `main` への push で、自動で公開する（変数でのスキップはしない）。事前に、Pages を有効にしておく必要がある（Settings → Pages → Source: GitHub Actions。public のリポジトリが要る）。

## テストの層

| 層 | 内容 | このプロジェクト |
| --- | --- | --- |
| 単体 | Kubernetes のクライアントを fake に差し替える | 実装済み。CI で実行 |
| envtest | 本物の `kube-apiserver` と `etcd` だけを起動する（kubelet もノードも無い） | 使わない。pull 用 Pod、`Node.status.images` は kubelet が無いと確かめられないため |
| e2e | kind（本物の kubelet と containerd）と、CI の中のローカルレジストリ | `test/e2e/run.sh` と `.github/workflows/e2e.yml`。**スキップが既定**（変数 `RUN_E2E` か手動で実行）。未実行 |

- e2e はローカルレジストリを使う: 同じタグに別の内容を 2 回 push すれば「リモートのタグが動いた」状況を作れる。rate limit の影響も受けない。認証が要る場合は、htpasswd 付きの 2 つ目のレジストリ（`localhost:5002`）を建て、kind のノードに、kubelet が読む `config.json` を置いて確かめる。
  kind のノードの containerd に、`localhost:5001` をローカルレジストリへ向ける設定を入れる（kind の公式の手順）。
- ノードの `status.images` の更新の遅れ（数分。pull のあとの状態を、pull 用 Pod が報告した digest から求める理由）は、kind でも再現されるはず。
  e2e は、ローカルの状態を読むステップの前に、`status.images` に反映されるまで待つ（`pull --dry-run` が `match` になるまで繰り返す）。
- マルチ arch は、`crane copy busybox:1.36` で index をそのままローカルレジストリへ写して確かめる（タグの digest と、ノードが報告する digest が同じか）。
- Knative（Revision の `imageDigest`）は、Knative Serving のインストールが重いので、e2e の対象外。単体テストのみ。

## 10. 実機で確認が必要な前提 [未確認]

実機で確認できたこと: `collect`、`pull`（pull 用 Pod、タグの付け替わり）、`trigger` の `mismatch` の検知、Knative の `rollout`（2026-10-14）。
未確認の点は、次のとおり。

1. （確認済み）pull 用 Pod（存在しないコマンド）で、使っているランタイムが pull だけ行い、`imageID` を返すこと。
2. （確認済み）`Node.status.images` の名前の並び（`repo:latest` と `repo@sha256:…` が同じエントリに載ること）。pull の数分後に反映された。
3. マルチアーキ: リモートで取る digest はタグ全体（index）の digest。ノードの名前や Pod の `imageID` も、同じ index の digest か。
   個別（platform）の digest が載るランタイムだと、新しいのに「不一致」と判定される。その場合は、ノードの OS とアーキに対応する
   個別の digest を引いて比べる方式に切り替える。アーキごとの digest は取れるが、今は使っていない。
   タグ全体の digest は、どのアーキが変わっても変わるので、「タグが動いたか」の検知は、これで足りる。
4. （確認済み）Knative: ksvc の `spec.template` への annotation で、新しい Revision ができ、タグが解決し直されること。
5. 非公開レジストリでの認証（digest 取得と pull）。e2e に、認証つきレジストリのシナリオがあるが、未実行。rate limit は [research.md](./research.md) のとおり、digest の取得は数えられない。
6. `Node.status.images` が 1 ノード 50 件まで（kubelet の既定）のため、全ノードの和集合で見ても、載らないことがある。

## 用語

| 用語 | 意味 |
| --- | --- |
| フローティングタグ | `latest` など、指す先が動くタグ。追う対象 |
| ローカルキャッシュ | 各ノードのイメージストア（4 章） |
| index digest | マルチアーキのタグ全体が指す digest。platform digest はアーキごとの個別のもの |
| 正 | 比較の基準。第一段階はリモート、第二段階はローカル |
| アクター / アクション | アクター（Actor）は、検知した差分に対して、指定されたアクション（Action）を実行する側。アクションは「何をするか」（`none`、`delete-pods` など） |
