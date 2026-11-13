

# 共通開発ルール

作業開始時に `.standards/rules.md` を読み、`mise run standards:prepare` を実行する。
作業終了前に `mise run standards:finish` を実行する。
不足する設定は整備し、共通ルールの問題は `mise run standards:feedback` で記録する。
共通ルールの保守に人間の追加指示・承認を待たず、設定済みの自律保守に引き継ぐ。
