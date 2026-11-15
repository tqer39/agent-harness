# agent-harness

> **開発終了・アーカイブ対象（2026-11-15）**
> 自作ハーネスの運用を終了し、履歴参照用として保存します。27 回の実行のうちマージ完了は 1 件、
> 停止は 20 件で、停止の多くがハーネス側の環境に起因したため、維持を続けない判断としました。
> GitHub 上のアーカイブは Terraform の PR のマージ時に適用します。
> 今後は Codex デスクトップを起点に、個人設定の
> [`claude-review` スキル](https://github.com/tqer39/dotfiles/pull/641)で PR 前に Claude の敵対的レビューを受けます。
> Claude 起点では [OpenAI 公式の `codex-plugin-cc`](https://github.com/openai/codex-plugin-cc) の
> `/codex:review`、`/codex:adversarial-review`、`/codex:rescue` を使います。レビューゲートは無効にします。
> 指摘は検証してから対応し、Orca はこの運用に使用しません。

人間が承認した受入条件をもとに、Codex の実装、Claude のレビュー、マージまでを進めるハーネスです。
Mac で動かします。

運用手順は [agent-harness の運用](docs/agent-harness.md)、設計は [仕様書](docs/superpowers/specs/2026-11-13-issue-driven-harness-design.md) を参照してください。

## 移行元

2026-11-13 に [tqer39/dev-standards](https://github.com/tqer39/dev-standards) から分離しました。
それ以前の履歴は dev-standards の[c6f0438c6e945d9b5b7a3e516d155d8b017471f8](https://github.com/tqer39/dev-standards/tree/c6f0438c6e945d9b5b7a3e516d155d8b017471f8)以前を参照してください。
