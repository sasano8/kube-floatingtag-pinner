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

## フラグ

| フラグ | 型 | 既定値 | 説明 |
| --- | --- | --- | --- |
| `--annotation` | string | `floatingtag-pinner/enabled` | annotation that marks target resources |
| `--daemon` | bool | `false` | keep running: repeat every --interval until stopped (default: run once and exit) |
| `--digest-length` | int | `12` | hex characters of a sha256 digest to show in the log, then "..." (0 = the whole digest) |
| `--dry-run` | bool | `false` | report what would be pulled, without pulling |
| `--floating-tags` | string | `latest` | comma-separated tags to track; images with other tags are ignored (empty = all tags) |
| `--interval` | duration | `5m0s` | with --daemon: time between passes |
| `--kubeconfig` | string | `-` | path to kubeconfig (default: in-cluster, then ~/.kube/config) |
| `--local` | string | `node` | local cache: node (container runtime image store of the nodes) |
| `--log-level` | string | `info` | log level threshold: debug\|info\|warn\|error (match, not-running = info; mismatch, not-pulled = warn; error = error) |
| `--log-states` | string | `-` | comma-separated states to log (default: all): match, mismatch, not-pulled, error; trigger also not-running |
| `--namespace` | string | `-` | namespace to watch (default: all) |
| `--on-new` | string | `register` | for images the local cache does not hold: register (pull) \| ignore |
| `--pull-namespace` | string | `default` | namespace for the short-lived Pods that make nodes pull images |
| `--pull-secret` | string | `-` | imagePullSecret name (in --pull-namespace) for private registries |
| `--pull-timeout` | duration | `2m0s` | per-node timeout for a pull |
