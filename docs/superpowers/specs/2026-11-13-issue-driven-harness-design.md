# Issue 起点のハーネスと作業状況の可視化

[2026-11-07 のハーネス設計](2026-11-07-claude-codex-harness-design.md) を改める。
依頼の起点を GitHub Issue にし、実装担当の実行方式を Herdr から Orca の端末と `codex exec` に替える。
あわせて、ハーネスを `dev-standards` から新しいリポジトリ `tqer39/agent-harness` に分離する。
この文書に書いていない事項（自動マージの条件、秘密情報の段階、実装担当とレビュー担当の分離、復旧、利用枠、評価）は、元の設計のまま変えない。

## 背景

元の設計には次の問題がある。

- Codex の作業状況が見えない。Herdr の画面は Mac の前でしか確認できない。
- スマートフォンから状況の確認と指示ができない。
- 人間の判断待ちと完了の通知が、GitHub の通知に埋もれる。
- 依頼の一覧がない。依頼の内容は Claude の会話にしか残らない。

## 要件

1. 作業の起点は常に人間とする。
2. 作業から派生した調査結果を残す。
3. 人間は Claude に依頼する。自然言語の読解は Claude を使う。
   1. 依頼は Mac Studio、Windows、Linux の PC から行う。
   2. スマートフォンからも確認できるようにする。
4. 人間の判断待ちと完了を、PC とスマートフォンに通知する。
5. 人間以外が Claude を動かす経路として、次を考慮する。
   1. Claude が別の Claude を呼び出す。
   2. 外部のアシスタント（Meta の muse、OpenAI の dots、xAI の Grok bot など）が Claude を動かす。
6. Claude が Codex を動かす。俯瞰は Claude、単一の作業の掘り下げは Codex が担う。
7. 依頼の状況をプロジェクト別に把握する。
8. 残タスクは、該当するリポジトリの Issue に起票する。
9. 共通の作業ルールは `dev-standards` に集約する。
10. LLM の作業効率は、OpenTelemetry のデータを `agent-telemetry` で集めて可視化する。
11. リポジトリの設定は `terraform-github` で管理する。

## 対象外

- Mac Studio 以外での実行。他の PC とスマートフォンは、Mac Studio の Claude に依頼する窓口として使う。
- Linear。台帳が GitHub で足りなくなったら、Linear の GitHub 同期で見る場所だけを移す。
- Entire などによる会話の全文の保存。
- Discord からの指示（双方向のやり取り）。
- 複数タスクの並列実行。
- 外部のアシスタントが Mac Studio の Claude を動かす経路と、そのための Claude 専用の GitHub App（「承認の偽装に対する扱い」を参照）。

## 全体の構成

```text
人間 ── PC（Mac Studio 本体、Windows と Linux は claude.ai/code の Remote Control）
     └ スマートフォン（Claude、GitHub、Orca、Discord の各アプリ）
        │ 依頼と質問
        ▼
Claude（Mac Studio）── 調査、要件の整理、受入条件の下書き
        │ Issue を作り、調査結果をコメントに残す
        ▼
GitHub Issue ◀── 外部のアシスタントも Issue を作れる
        │ 人間が approved ラベルを付ける
        ▼
agent-harness watch（launchd）── 承認済みの Issue を探す
        │
        ▼
agent-harness run（Orca の端末）
   ├ 実装担当: codex exec --json
   ├ レビュー担当: claude -p
   ├ PR の作成とマージ（Closes #N）
   ├ 残タスクの起票
   └ 状態の変化 → Issue のラベルとコメント、Orca の表示、Discord
```

| 要素 | 役割 |
| --- | --- |
| GitHub Issue と Projects | 台帳。依頼、調査結果、受入条件、承認、結果を記録する |
| Orca | 作業中の画面。実装とレビューのログを Mac とスマートフォンで見る |
| Discord | 呼び出し。人間の判断待ち、完了、異常を PC とスマートフォンに届ける |
| `agent-telemetry` | 改善。Claude と Codex の OpenTelemetry と会話の記録を集める |
| `agent-harness` | ハーネス本体、Claude の skill と権限ルール、Codex の権限プロファイル |
| `dev-standards` | リポジトリに適用する共通ルール |
| `terraform-github` | 全リポジトリのラベル |

## リポジトリの分離

`dev-standards` の役割は、リポジトリへのルールの適用と、その保守である。
ハーネスは役割が異なるので、`tqer39/agent-harness` に分離する。

- ハーネスのコードは、`dev-standards` の他のコードに依存せず、他のコードからも使われていない。
- `dev-standards` の CLI は macOS、Linux、Windows を対象にする。ハーネスは Mac Studio の常駐サービスになる。
- ハーネスの変更で、利用先に配る共通ルールの版を上げずに済む。
- 自律保守が更新する `dev-standards` と、ハーネス本体の変更を分けられる。

Claude の skill と権限ルールは、リポジトリ単位ではなくユーザー単位（`~/.claude`）の設定である。
Codex の権限プロファイルと同じく、`agent-harness setup` で配置する。

分離では動作を変えずにコードを移し、この設計の変更は分離後の `agent-harness` で行う。
Git の履歴は移さず、移行元の `dev-standards` のコミットを記録する。

## 役割と権限の変更

元の設計の表から、次の行を変える。

| 担当 | 役割 | GitHub の操作 |
| --- | --- | --- |
| 人間 | 受入条件の承認（`approved` ラベル）、停止したタスクの判断 | すべて |
| Claude（要件定義） | 調査、要件と受入条件の整理、Issue の作成 | Issue の作成とコメント。`approved` ラベルの付与は禁止 |
| ハーネス | 承認の確認、Issue のラベルとコメント、残タスクの起票に加え、元の設計の操作 | GitHub App の権限内 |

GitHub App に `issues` の書き込み権限を追加する。
ハーネスは引き続き `~/.config/gh/hosts.yml` のトークンを使わない。

## Issue の形式

Issue のタイトルが PR のタイトルになる。
本文は次の節で構成する。Claude の skill がこの形式で作る。

```markdown
## タスク

（実装担当に渡す内容）

## 種別

feature | bugfix | refactor

## 受入条件

- [ ] 1 行に 1 条件

## 受入テスト

（固定する受入テストがあれば、git apply で適用できる差分を diff のコードブロックで書く）
```

元の `run` の `--task-file`、`--criteria-file`、`--kind`、`--acceptance-patch` は、それぞれ本文の節から読む。
承認は本文全体に対して行うので、受入テストも承認の対象になる。

調査結果は Issue のコメントに残す。
長く参照する結論だけを、リポジトリの `docs` に入れる。

## ラベルと状態

ラベルは `terraform-github` で全リポジトリに作る。

| ラベル | 付ける人 | 意味 |
| --- | --- | --- |
| `approved` | 人間だけ | 受入条件を承認した |
| `agent:running` | ハーネス | 着手済み。二重の着手を防ぐ |
| `needs-human` | ハーネス | 人間の判断待ち。理由はコメントに書く |

完了は、PR のマージで Issue が閉じた状態で表す。
未承認はラベルがない状態で表す。
失敗は理由を書いて `needs-human` に揃える。

GitHub Projects を 1 つ作り、Claude とハーネスが Issue を追加する。
表はリポジトリ別にまとめ、ラベルの列で段階を見る。
Projects の状態欄はラベルと同期しない。同じ情報を 2 か所で持たないためである。

## 承認の確認

`watch` の確認は着手の候補を絞るためのものとし、`run` が着手の直前に改めて確認する。
`run` は 1 回の GraphQL の照会で、本文、本文の最終編集時刻（`lastEditedAt`）、ラベルの付与の履歴を取得し、この 1 つのスナップショットで承認を確認する。
実装担当に渡すタスク、受入条件、受入テストは、同じスナップショットの本文から作る。
本文のハッシュを実行記録と Issue のコメントに残し、着手後に本文が編集されても実行中のタスクには反映しない。
確認と取得の間に本文が差し替えられ、承認されていない受入条件で実行されることを防ぐためである。

ハーネスは、次をすべて満たす Issue だけに着手する。

- 開いている。
- `approved` が付いていて、`agent:running` と `needs-human` が付いていない。
- Issue のタイムラインで、最後に `approved` を付けたアカウントが設定した人間のアカウントである。
- 最後に `approved` が付いた時刻より後に、本文が編集されていない。

承認の後に本文が編集されていたら、着手せずに `approved` を外し、再承認を求めるコメントと通知を送る。
外部のアシスタントや Claude が、承認後に受入条件を差し替えることを防ぐためである。

外部のアシスタントが作った Issue の本文は、信頼できない入力として扱う。
人間が承認するまで実装担当に渡さない。

### 承認の偽装に対する扱い

Claude は人間と同じ `gh` の認証で動くので、タイムラインだけでは人間と Claude を区別できない。
Claude の権限ルールで `approved` を付けるコマンドを拒否するが、権限ルールはコマンドの文字列で判定するので、`gh api` など別の形式は防げない。
この残るリスクは、次の理由と制限のもとで受け入れる。

- 同じ認証の Claude は、承認を経なくても GitHub に直接書き込める。元の設計でも、`gh pr merge` の拒否は完全な境界でないと認めている。承認ラベルだけを守っても、境界は強くならない。
- 承認が偽装されても、元の設計の自動マージの条件はすべて適用される。秘密情報スキャン、セキュリティ関連パスと人間の確認が必要なパスでの停止、必須チェック、別モデルのレビュー、すべての受入条件の判定を通らなければマージされない。
- 着手を Discord に通知する。人間が承認していない着手に気づけるようにするためである。
- 外部のアシスタントは、自分の認証で Issue を作るところまでとする。外部のアシスタントが Mac Studio の Claude を動かす経路（要件 5.2）は、Claude 専用の認証を導入するまで有効にしない。外部からの指示で Claude が承認を偽装することを防ぐためである。
  Claude 専用の認証では、Claude の書き込みを Claude 専用の GitHub App で行い、Claude の環境から人間の `gh` の認証を外して、承認者の確認で Claude を区別できるようにする。

## 着手のきっかけ

`agent-harness watch` を launchd のユーザーエージェントとして常駐させる。
既定では 3 分ごとに、GitHub App のインストール先のリポジトリから承認済みの Issue を探す。
インストール先だけを対象にするので、App のインストールがそのまま対象リポジトリの許可リストになる。

着手するときは `agent:running` を付けてから、次のように Orca の端末で `run` を起動する。

```sh
orca terminal create --worktree path:<作業用 clone> --title "#<N> <タイトル>" \
  --command "agent-harness run --issue <owner>/<repo>#<N>"
```

`orca` に接続できないときは、端末を作らずに `run` を直接起動し、ログをファイルに書く。
Orca は見るための画面であり、実行の前提にしない。

同時に実行するタスクは、全体で 1 件とする。
元の設計の「1 リポジトリにつき 1 タスク」より厳しく、利用枠の消費も抑えられる。

GitHub Actions のセルフホストランナーでラベルのイベントを受ける方式は採らない。
公開リポジトリにつないだ Mac Studio が、外部の PR から攻撃される余地を作るためである。

## 実装担当の実行

Herdr の代わりに `codex exec` を使う。
2026-11-13 の検証で、次の構成が権限プロファイルの制限を保つことを確かめた。

```sh
codex -p agent-harness exec --json -m <model> -c model_reasoning_effort="<effort>" \
  -c 'projects."<作業用 clone>".trust_level="trusted"' \
  --output-schema <結果のスキーマ> -o <作業用 clone>/.agent-run/result.json "<指示>"
```

- ハーネスが子プロセスとして起動し、環境変数は固定した一覧と明示した値だけを渡す（`env -i` と同じ効果）。Orca の端末が設定する `ORCA_*`（起動トークンを含む）を実装担当に渡さない。
- 結果は `--output-schema` で形を固定し、`-o` で従来と同じ `.agent-run/result.json` に書く。画面を読んで完了を待つ仕組みは廃止する。
- ハーネスは標準出力の JSONL を読み、`events.jsonl` に保存しながら 1 行ずつの表示に変えて、自分の標準出力に流す。コマンド、終了コード、変更したファイル、エージェントのメッセージ、エラーを表示する。パスは作業用 clone からの相対パスで表示する。
- 修正の依頼と人間の追加指示は `codex exec resume <thread_id>` で同じ thread に送る。thread_id は実行記録に保存する。
- 完了は、プロセスの終了と結果ファイルの検証で判定する。画面の文字列は読まない。
- 利用上限とエラーは、`turn.failed` と `error` のイベントで判定する。形式が未確認のものは、エラーの本文を付けて `needs-human` にする。

レビュー担当の `claude -p --json-schema` は変えない。出力は同じ Orca の端末に表示する。

### Orca の設定

Orca のエージェントのフック（`agentStatusHooksEnabled`）は無効にする。
有効にすると、Orca が `~/.codex/hooks.json`、`~/.codex/config.toml`、`~/.claude/settings.json` などを書き換えるためである。

Orca の Chat UI は実装担当に使わない。2026-11-13 の検証で次を確認した。

- 既定の権限モードが `full-access`（サンドボックスなし、承認なし）である。
- 既定の引数に `-p agent-harness` を指定しても `codex app-server` に渡らず、権限プロファイルが効かない。
- セッションの権限モードが、ターンごとにサンドボックスの設定を上書きする。
- `ORCA_*` の環境変数がエージェントに渡る。
- CLI からチャットのセッションを読み書きできない。

### Orca の表示

ワークツリーの表示名を `#<N> <タイトル>` にし、段階を次のように対応させる。理由は Orca のコメントに書く。

| ハーネスの状態 | Orca の段階 |
| --- | --- |
| 実装中 | `in-progress` |
| レビュー中 | `in-review` |
| `needs-human` | `todo` |
| 完了 | `completed` |

## 停止と再開

`needs-human` で止めるときは、`approved` と `agent:running` を外して `needs-human` を付け、理由を Issue にコメントする。
PR がある場合は、元の設計どおり PR にも `needs-human` を付ける。

人間は Issue へのコメントか Claude を通して指示を足し、`needs-human` を外して `approved` を付け直す。
`watch` は次の確認で `resume` を起動する。
実装担当には、停止後に人間のアカウントが書いたコメントだけを追加指示として渡す。
他のアカウントのコメントは渡さない。

利用上限で止まったときは `agent:running` を残し、Issue にコメントして通知する。
`watch` は 30 分ごとに `resume` を試す。承認の内容は変わっていないので、人間の操作を待たない。

## 通知

ハーネスと `watch` が Discord の webhook に送る。

| 契機 | 送り手 | 内容 |
| --- | --- | --- |
| 着手 | `run`（起動の経路を問わない） | Issue のタイトルとリンク |
| `needs-human` で停止 | ハーネス | 理由の 1 行と Issue のリンク |
| マージと確認の完了 | ハーネス | 要約の 1 行と PR のリンク |
| 利用上限で停止 | ハーネス | 停止した旨と Issue のリンク |
| 着手の見送り（承認者の不一致、承認後の編集） | `watch` | 再承認が必要な旨と Issue のリンク |
| `watch` 自身の異常（認証切れなど） | `watch` | エラーの 1 行 |

- 途中経過は通知しない。Orca と Issue で見る。着手だけは、承認していない着手に気づくために通知する。
- 本文には要約とリンクだけを載せ、コードとログを載せない。
- webhook の URL は 1Password の Vault `agent-harness` に置き、ハーネスが段階 1 の認証で読む。実装担当には渡らない。
- 送信に失敗してもハーネスは止めない。記録は Issue に残る。
- 元の設計の「通知は GitHub の通知を使う」を置き換える。

## 観測

- 実装担当の OpenTelemetry の送信先を、権限プロファイルに設定する。
- `OTEL_RESOURCE_ATTRIBUTES` で `agent.harness=true`、リポジトリ、Issue 番号、`run_id` を渡し、手作業のセッションと区別して集計できるようにする。
- レビュー担当は `~/.claude/settings.json` の設定を使い、同じ属性を付ける。
- 各実行の要約（所要時間、修正回数、トークン量、結果）を Issue にコメントする。

## スマートフォンからの利用

| 目的 | 手段 |
| --- | --- |
| Claude への依頼と質問 | Claude アプリ（Remote Control） |
| 承認と結果の確認 | GitHub アプリ |
| 作業中の画面 | Orca アプリ（ペアリング済み、LAN または Tailscale） |
| 通知 | Discord アプリ |

Orca のスマートフォンへの通知は使わない。
検証では、`orca worktree set` による段階の変更と、端末のベル（BEL）のどちらでも通知が届かなかった。

## 移行

最初に、動作を変えずにハーネスを `agent-harness` へ分離する。
その後、`agent-harness` で次の変更を行う。

1. `Herdr` を `codex exec` による実行に置き換え、イベントの表示を追加する。
2. `run --issue` を追加し、承認の確認、ラベル、コメント、残タスクの起票を実装する。`--approved-by-human` は廃止する。
3. `watch` と launchd の設定ファイル、登録と解除の手順を追加する。
4. Discord の通知を追加する。
5. 権限プロファイルに OpenTelemetry の設定を加え、属性を渡す。
6. `~/.claude/skills/agent-harness/SKILL.md` を `agent-harness` に移し、`setup` で配置する。役割を「Issue の作成と受入条件の記述、人間への承認の依頼」に変え、ハーネスの同期実行をやめる。
7. Claude の権限ルールに `approved` の付与の拒否を追加し、`setup` で配置する。
8. 運用文書と構成図（`.infra`）を更新する。

`agent-harness` の外で行う作業は次のとおりである。

- `terraform-github` で `agent-harness` リポジトリを管理対象にし、3 つのラベルを全リポジトリに追加する。別の PR とし、マージは人間が判断する。
- GitHub App に `issues` の書き込み権限を追加する。人間が行う。
- GitHub Projects を作る。人間が行う。
- Discord に通知用のチャンネルと webhook を作り、URL を Vault に保存する。人間が行う。

## 検証項目

元の設計の検証項目に加える。
Herdr に関する項目（`hc` による起動、`hc read` による利用上限の検知、信頼確認への応答）は削除する。

| 項目 | 合格の条件 | 現状 |
| --- | --- | --- |
| `codex exec` での制限 | `env -i` と権限プロファイルで、GitHub への接続、HOME への書き込み、拒否リストの読み取りが拒否され、許可したドメインには接続できる | 確認済み |
| `ORCA_*` の遮断 | 実装担当の環境に `ORCA_*` がない。サンドボックスから `orca` を使えない | 確認済み |
| 結果の固定と再開 | `--output-schema` と `-o` で結果が書かれ、`exec resume` で thread と権限プロファイルが保たれる | 確認済み |
| Orca の表示 | 端末のログと段階の変化がスマートフォンで見える | 確認済み |
| Orca のフック無効時の副作用 | `~/.codex` と `~/.claude` が書き換わらない | 確認済み |
| 利用上限のイベント | 利用上限に達したときのイベントの形式を確認し、判定できる | 未検証 |
| launchd からの起動 | `watch` から `orca terminal create` と 1Password の読み取りができる | 未検証 |
| 承認の確認 | 人間以外が付けた `approved` と、承認後の本文の編集で着手しない。実装担当への入力が、承認を確認したスナップショットの本文と一致する | 未検証 |
| OpenTelemetry | ハーネスの実行分が `agent-telemetry` に属性付きで届く | 未検証 |
| Discord | PC とスマートフォンに通知が届く | 未検証 |
