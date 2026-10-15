# pinner trigger

stage 2: find resources running a different image than the local cache, and run their action

第二段階。ローカルキャッシュを正にして、実行中の Pod（Knative は Revision）の digest と比べ、違えば、リソースの annotation で指定されたアクションを依頼する。

アクションは、`floatingtag-pinner/on-mismatch`（任意で `on-not-pulled`、`on-error`）で指定する。未指定は `none`（何もしない）。
組み込みのアクションは `none`、`rollout`、`delete-pods`。依頼したあとの結果は追わない（投げっぱなし）。

見つけたことは、アクションと関係なく、常にログに出る（`trigger match`、`trigger mismatch` など）。

## 使い方

```
pinner trigger [flags]
```

フラグは、環境変数でも渡せる（`PINNER_` + フラグ名の大文字、`-` は `_`）。優先順位は、コマンドライン、環境変数、既定値の順。

## フラグ

| フラグ | 環境変数 | 型 | 既定値 | 説明 |
| --- | --- | --- | --- | --- |
| `--annotation` | `PINNER_ANNOTATION` | string | `floatingtag-pinner/enabled` | annotation that marks target resources |
| `--daemon` | `PINNER_DAEMON` | bool | `false` | keep running: repeat every --interval until stopped (default: run once and exit) |
| `--debug` | `PINNER_DEBUG` | bool | `false` | shorthand for --log-level=debug: also log how each node pulled, and what the nodes and the Pods report |
| `--digest-length` | `PINNER_DIGEST_LENGTH` | int | `12` | hex characters of a sha256 digest to show in the log, then "..." (0 = the whole digest) |
| `--dry-run` | `PINNER_DRY_RUN` | bool | `false` | run no action: log which action would have run |
| `--floating-tags` | `PINNER_FLOATING_TAGS` | string | `latest` | comma-separated tags to track; images with other tags are ignored (empty = all tags) |
| `--interval` | `PINNER_INTERVAL` | duration | `5m0s` | with --daemon: time between passes |
| `--kubeconfig` | `PINNER_KUBECONFIG` | string | `-` | path to kubeconfig (default: in-cluster, then ~/.kube/config) |
| `--local` | `PINNER_LOCAL` | string | `node` | local cache: node (container runtime image store of the nodes) |
| `--log-level` | `PINNER_LOG_LEVEL` | string | `info` | log level threshold: debug\|info\|warn\|error (match, not-running = info; mismatch, not-pulled = warn; error = error) |
| `--log-states` | `PINNER_LOG_STATES` | string | `-` | comma-separated states to log (default: all): match, mismatch, not-pulled, error; trigger also not-running |
| `--max-actions` | `PINNER_MAX_ACTIONS` | int | `0` | most actions to request per pass (0 = no cap) |
| `--namespace` | `PINNER_NAMESPACE` | string | `-` | namespace to watch (default: all) |
