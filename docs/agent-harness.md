# agent-harness の運用

設計は [仕様書](superpowers/specs/2026-11-07-claude-codex-harness-design.md) を参照する。

## 初回の準備

```sh
mise run build
bin/agent-harness setup
scripts/agent-harness-probe.sh
bin/agent-harness doctor --repo <owner>/<name>
```

1Password の Vault、サービスアカウント、GitHub App は人間が作成する。

### 権限プロファイルを更新する

`bin/agent-harness` に埋め込んだ権限プロファイルが変わると、`doctor` の「Codex の権限プロファイル」が FAIL になる。
差分を確認してから、人間が次を実行する。API キー用ホームの複製も更新する。
Codex が書き足す作業用 clone（`*-worktrees/agent-<run_id>`）の `trust_level = "trusted"` だけの `projects` 節は、比較で無視する。

```sh
mv ~/.codex/agent-harness.config.toml ~/.codex/agent-harness.config.toml.bak
bin/agent-harness setup
scripts/agent-harness-probe.sh
bin/agent-harness setup-api-key   # API キーで実行する場合だけ
```

## タスクの実行

人間が受入条件を承認してから実行する。

```sh
bin/agent-harness run \
  --repo <owner>/<name> --title 'feat: 変更の要約' --kind feature \
  --task-file task.md --criteria-file criteria.md --approved-by-human
```

固定する受入テストがあれば `--acceptance-patch tests.patch` を追加する。
実装担当とレビュー担当の経過は、`run` を実行した端末に 1 行ずつ表示する。
実装担当のイベントは `~/.local/state/agent-harness/runs/<run_id>.events.jsonl` に残る。
実装担当、レビュー担当、検証のコマンドは別のプロセスグループで動く。
`run`、`resume`、`reconcile` は Ctrl-C、SIGTERM、端末を閉じたとき（SIGHUP）に子プロセスをグループごと止める。
実行記録は `implementing` のまま残るので、`resume` で同じ thread から続けられる。
2 回目の Ctrl-C は即時に終了する。
子プロセスは端末から見てバックグラウンドで動くので、端末を直接読む入力（パスワードやパスフレーズの入力など）を求めるコマンドは止まったままになる。
無人で動かす前提なので、認証は事前に済ませておく。

`run` は作業の前に、mise の Codex CLI（`npm:@openai/codex`）が古ければ `mise upgrade` で更新する。更新できなくても、今の版でモデルを使えれば続ける。
`run` と `doctor` は、`--model`（既定は `gpt-6.1-sol`）を `codex debug models` の一覧で確認する。
使えるモデルは認証方式と Codex CLI の版で変わる。一覧に無い場合は Codex CLI を更新するか、`--model` で一覧のモデルを指定する。

利用先の checkout に `.standards/cache/dev-standards` があれば、作業用 clone にも複製する。
複製しないと、`mise run check` の `standards:check` が失敗する。

| 終了コード | 意味 | 対応 |
| --- | --- | --- |
| 0 | マージと確認が完了、または revert が完了 | 表示された状態を確認する |
| 3 | 利用枠の解除待ち | 解除後に `bin/agent-harness resume --run <run_id>` |
| 4 | 人間の判断待ち | PR の `needs-human` と理由を確認する。auto-merge の待機が時間切れの場合、後でマージされても次の実行か `reconcile` で検証する |
| 1 | その他の失敗 | 表示された内容を確認する |

## API キーで実行する

Codex の利用枠が尽きている間は、OpenAI の API キーで実行できる。従量課金なので、明示したときだけ使う。

1. OpenAI で上限を設定した専用プロジェクトのキーを作り、1Password の Vault `agent-harness` の項目 `openai` のフィールド `api-key` に保存する。
2. 人間が次を実行する。キーは 1Password から標準入力で渡され、画面に表示されない。

```sh
bin/agent-harness setup-api-key
bin/agent-harness doctor --repo <owner>/<name> --codex-auth api-key
```

実行時は `run` に `--codex-auth api-key` を付ける。`resume` は記録された認証方式を使う。

キーは `~/.local/state/agent-harness/codex-api-home/auth.json` に保存する。`~/.codex` の ChatGPT ログインと自律保守には影響しない。このセッションの記録は `~/.codex/sessions` に入らないので、`agent-telemetry` のセッションの集計には含まれない。

使い終わったら次を実行し、OpenAI 側でキーを取り消す。

```sh
CODEX_HOME=~/.local/state/agent-harness/codex-api-home codex logout
```

## 停止と再開

main の CI が失敗すると、そのリポジトリの自動処理を止めて revert する。
人間は解除の前に次を確認する。

- revert PR がマージされた、または人手で main を復旧した。
- 未完了の revert PR を閉じた。
- main の CI が成功している。
- 失敗の原因を把握した。

確認できたら、人間が次を実行する。

```sh
bin/agent-harness clear-stop --repo <owner>/<name>
```

Mac の停止などでマージ後の確認が途中で終わった場合は、次を実行する。

```sh
bin/agent-harness reconcile --repo <owner>/<name>
```

auto-merge を有効にする前に止まった PR には `needs-human` が付く。確認してマージするか、閉じる。マージした場合も、次の実行か `reconcile` で main の CI を確認する。

## 実行記録

`~/.local/state/agent-harness/runs/<run_id>.json` に保存する。

## リポジトリ固有の設定

基準ブランチの `.agent-harness/policy.json` で追加できる。既定の停止パスは削除できない。

```json
{
  "stop_paths": ["billing/**"],
  "verify": ["mise", "run", "check"],
  "required_check": "workflow-result"
}
```

### CodeRabbit

ハーネスの PR は、CodeRabbit なしで運用する。レビューは別モデルの 1 段だけである。

CodeRabbit は、シートのない GitHub App が作成した PR をレビューしない。
`@coderabbitai review` を送っても「bot にシートが割り当てられていない」と返すだけである。
レビューには、CodeRabbit の Team Management で App の bot にシートを割り当てる必要がある。

外部レビューの完了は既定で待たない。未解決のスレッドは毎回確認し、人間のコメントがあれば止まる。
CodeRabbit は無料枠の範囲だけで使う。rate limit になっても待機や再依頼はせず、CodeRabbit なしで次に進む。

シートを割り当てたリポジトリでは、`external_review_check` と `external_review_authors` を指定できる。
ただし、スキップや上限到達でも check は `completed` になり（`skipped` や `neutral`）、ハーネスは結果を区別しない。
レビューが実行されなくても次に進むので、有効にする前に実機で待ち方を確認する。

## 関連リポジトリの改善

`agent-telemetry`、`omnodi`、`terraform-github` に改善が必要なときは、都度 PR を出す。
`terraform-github` はマージで適用まで進むので、PR の作成までとする。
