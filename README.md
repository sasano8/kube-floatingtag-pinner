# kube-floatingtag-pinner

`latest` などの可変タグ（floating tag）が指す実体（digest）を追跡し、変化を検知して、
新しいイメージで動かし直すための仕組み。ラフスケッチ段階（詳細は [IDEA.md](./IDEA.md)）。

## 何を解決するか

- `image: xxx:latest` のままだと、レジストリ側が更新されても Kubernetes 側の spec は変わらず、
  新しいイメージが使われない（Knative は Revision 作成時に digest を固定するため特に顕著）。
- Deployment でも、Pod ごとに起動時期で digest がずれうる。
- そこで「タグが今どの digest を指しているか」を記録し、変わったら検知・反映する。

## 提供する機能

| 段階 | 機能 |
| --- | --- |
| 第一段階 | 収集・差分検知・取得（ワークロードは変更しない） |
| 第二段階 | 検知した差分に対する反映（デプロイヤー） |

### 登場人物

| 名前 | 役割 | 第一段階 | 第二段階 |
| --- | --- | :---: | :---: |
| リモートレジストリ | タグ → digest の正本（Docker Hub など） | ○ | |
| ローカルレジストリ | Kubernetes 上のストア（例: ConfigMap）。前回までのタグ → digest を保持 | ○ | |
| Kubernetes リソース | 所定の annotation が付いた Deployment / StatefulSet / DaemonSet / Knative Service。参照元 | ○ | ○ |
| コレクター | リソースからイメージを収集し、ローカルレジストリとの差分を検知、リモートレジストリから取得 | ○ | ○ |
| デプロイヤー | 検知した差分に対し適切な処置（spec の image に digest を書く等）を行う | | ○ |

> 第二段階ではリモートレジストリとの直接のやり取りは行わず、コレクターが検知した差分を入力とする。

## 概要図

### 第一段階: 収集と差分検知

```mermaid
flowchart LR
    K["Kubernetes リソース<br/>(annotation 付き)"]
    C["コレクター"]
    L[("ローカルレジストリ<br/>(k8s 上のストア)")]
    R[("リモートレジストリ")]

    C -- "① annotation 付きリソースを列挙<br/>イメージ(タグ)を収集" --> K
    C -- "② 前回の digest を参照" --> L
    C -- "③ 現在の digest を取得 (pull)" --> R
    C -- "④ 差分があれば更新・記録" --> L
```

### 第二段階: 反映

```mermaid
flowchart LR
    K["Kubernetes リソース<br/>(annotation 付き)"]
    C["コレクター"]
    D["デプロイヤー"]

    C -- "① イメージを収集<br/>差分を検知" --> K
    C -- "② 差分を通知" --> D
    D -- "③ 適切な処置<br/>(image に digest を反映 等)" --> K
```

## 動作の流れ（第一段階）

1. コレクターが定期実行され、所定の annotation が付いたリソースを列挙する。
2. Pod テンプレートの `containers[]` / `initContainers[]` からイメージを取り出し、重複を除く。
3. ローカルレジストリに記録済みの digest と、リモートレジストリの現在の digest を比較する。
4. 変わっていれば差分として検知し、ローカルレジストリを更新する（イメージ・参照元・前後の digest を残す）。

## 既存ツールの対応状況

Keel・Knative 本体・Argo CD Image Updater では、ksvc の `latest` 追従を満たせないことを確認している
（Knative 本体は実機で確認）。詳細は [docs/research/existing-tools.md](./docs/research/existing-tools.md)。

## 未決事項

- annotation のキー名（IDEA.md では仮に `image-resolver/enabled: "true"`）。
- ローカルレジストリの実体（ConfigMap / CRD など）。
- 実装形態（最小は CronJob + `crane` + `kubectl`、必要に応じてコントローラ化）。
- Argo CD の `selfHeal` との折り合い（第二段階の実運用前に検討。IDEA.md 参照）。
