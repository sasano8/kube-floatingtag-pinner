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

## フラグ

| フラグ | 型 | 既定値 | 説明 |
| --- | --- | --- | --- |
| `--annotation` | string | `floatingtag-pinner/enabled` | annotation that marks target resources |
| `--daemon` | bool | `false` | keep running: repeat every --interval until stopped (default: run once and exit) |
| `--debug` | bool | `false` | shorthand for --log-level=debug: also log how each node pulled, and what the nodes and the Pods report |
| `--digest-length` | int | `12` | hex characters of a sha256 digest to show in the log, then "..." (0 = the whole digest) |
| `--dry-run` | bool | `false` | run no action: log which action would have run |
| `--floating-tags` | string | `latest` | comma-separated tags to track; images with other tags are ignored (empty = all tags) |
| `--interval` | duration | `5m0s` | with --daemon: time between passes |
| `--kubeconfig` | string | `-` | path to kubeconfig (default: in-cluster, then ~/.kube/config) |
| `--local` | string | `node` | local cache: node (container runtime image store of the nodes) |
| `--log-level` | string | `info` | log level threshold: debug\|info\|warn\|error (match, not-running = info; mismatch, not-pulled = warn; error = error) |
| `--log-states` | string | `-` | comma-separated states to log (default: all): match, mismatch, not-pulled, error; trigger also not-running |
| `--max-actions` | int | `0` | most actions to request per pass (0 = no cap) |
| `--namespace` | string | `-` | namespace to watch (default: all) |
