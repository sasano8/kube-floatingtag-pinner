# pinner pull

stage 1: pull into the local cache (node image stores) what changed on the remote registry

第一段階。リモートレジストリを正にして、ローカルキャッシュ（各ノードのイメージストア）を同期する。

リモートの、フローティングタグ（既定は `latest`）が指す digest と、ローカルの同じタグが指す digest を比べ、違えば、kubelet に pull させる。
pull のあとにローカルを読み直し、状態の変化（`mismatch` → `match`）として報告する。リソースには何もしない。

ログの行は、`pull match`、`pull mismatch` のように、段階の接頭辞で始まる。`outcome` は、pull がしたこと（`pulled`、`would-pull`、`ignored`、`pull-failed`）。

## 使い方

```
pinner pull [flags]
```

フラグは、環境変数でも渡せる（`PINNER_` + フラグ名の大文字、`-` は `_`）。優先順位は、コマンドライン、環境変数、既定値の順。

## フラグ

| フラグ | 環境変数 | 型 | 既定値 | 説明 |
| --- | --- | --- | --- | --- |
| `--annotation` | `PINNER_ANNOTATION` | string | `floatingtag-pinner/enabled` | annotation that marks target resources |
| `--credential` | `PINNER_CREDENTIAL` | string | `-` | file in docker config.json format with the registry credentials to look up the remote digest (empty: none; default: the default keychain) |
| `--daemon` | `PINNER_DAEMON` | bool | `false` | keep running: repeat every --interval until stopped (default: run once and exit) |
| `--debug` | `PINNER_DEBUG` | bool | `false` | shorthand for --log-level=debug: also log how each node pulled, and what the nodes and the Pods report |
| `--digest-length` | `PINNER_DIGEST_LENGTH` | int | `12` | hex characters of a sha256 digest to show in the log, then "..." (0 = the whole digest) |
| `--dry-run` | `PINNER_DRY_RUN` | bool | `false` | report what would be pulled, without pulling |
| `--floating-tags` | `PINNER_FLOATING_TAGS` | string | `latest` | comma-separated tags to track; images with other tags are ignored (empty = all tags) |
| `--interval` | `PINNER_INTERVAL` | duration | `5m0s` | with --daemon: time between passes |
| `--kubeconfig` | `PINNER_KUBECONFIG` | string | `-` | path to kubeconfig (default: in-cluster, then ~/.kube/config) |
| `--local` | `PINNER_LOCAL` | string | `node` | local cache: node (container runtime image store of the nodes) |
| `--log-level` | `PINNER_LOG_LEVEL` | string | `info` | log level threshold: debug\|info\|warn\|error (match, not-running = info; mismatch, not-pulled = warn; error = error) |
| `--log-states` | `PINNER_LOG_STATES` | string | `-` | comma-separated states to log (default: all): match, mismatch, not-pulled, error; trigger also not-running |
| `--namespace` | `PINNER_NAMESPACE` | string | `-` | namespace to watch (default: all) |
| `--on-new` | `PINNER_ON_NEW` | string | `register` | for images the local cache does not hold: register (pull) \| ignore |
| `--pull-namespace` | `PINNER_PULL_NAMESPACE` | string | `-` | namespace for the short-lived Pods that make nodes pull images (default: the namespace pinner runs in, or "default" outside a cluster) |
| `--pull-timeout` | `PINNER_PULL_TIMEOUT` | duration | `2m0s` | per-node timeout for a pull |
