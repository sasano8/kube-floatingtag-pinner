# pinner version

print which build this is (commit and Go version)

どのビルドの `pinner` かを表示する（コミット、Go のバージョン）。不具合を報告するときや、
「修正前のバイナリを使っていないか」を確かめるときに使う。`--version` でも同じ。

## 使い方

```
pinner version [flags]
```

フラグは、環境変数でも渡せる（`PINNER_` + フラグ名の大文字、`-` は `_`）。優先順位は、コマンドライン、環境変数、既定値の順。

## フラグ

| フラグ | 環境変数 | 型 | 既定値 | 説明 |
| --- | --- | --- | --- | --- |
