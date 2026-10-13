# pinner collect

list the annotated resources and the images they use (the base of pull and trigger)

annotation の付いたリソースと、そのリソースが使っているイメージを集めて表示する。
`pull` と \`trigger` は、どちらも、この収集から始まる。読み取りのみで、何も変更しない。

- 1 行が、（リソース、イメージ）。`TRACKED` が `yes` のものが、`pull` と `trigger` の対象。
- 対象外のイメージは、理由が出る（digest 指定、フローティングタグでない）。
- リソースの annotation に書かれたアクション（`ON-MISMATCH` など）も出る。`-` は未指定（既定の `none`）。

## 使い方

```
pinner collect [flags]
```

## フラグ

| フラグ | 型 | 既定値 | 説明 |
| --- | --- | --- | --- |
| `--annotation` | string | `floatingtag-pinner/enabled` | annotation that marks target resources |
| `--floating-tags` | string | `latest` | comma-separated tags to track; images with other tags are ignored (empty = all tags) |
| `--kubeconfig` | string | `-` | path to kubeconfig (default: in-cluster, then ~/.kube/config) |
| `--namespace` | string | `-` | namespace to watch (default: all) |
| `-o` | string | `table` | shorthand for --output |
| `--output` | string | `table` | output format: table\|json |
