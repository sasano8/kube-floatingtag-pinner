# コマンドライン

このページは、コードのフラグの定義から生成している（`pinner docs`）。手で編集しない。

```
pinner <command> [flags]
```

| コマンド | 内容 |
| --- | --- |
| [`pinner collect`](./collect.md) | list the annotated resources and the images they use (the base of pull and trigger) |
| [`pinner pull`](./pull.md) | stage 1: pull into the local cache (node image stores) what changed on the remote registry |
| [`pinner trigger`](./trigger.md) | stage 2: find resources running a different image than the local cache, and run their action |
| [`pinner version`](./version.md) | print which build this is (commit and Go version) |
