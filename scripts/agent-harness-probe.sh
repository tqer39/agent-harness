#!/usr/bin/env bash
# 実装担当のサンドボックスが仕様どおりに制限するかを確認する。
# 認証情報は読み取らない。拒否を期待する項目は、読めた場合でも内容を出力しない。
set -u

work="$(mktemp -d "$HOME/.agent-harness-probe.XXXXXX")"
trap 'rm -rf "$work"' EXIT
git init -q -b main "$work"
git -C "$work" -c user.name=probe -c user.email=probe@example.invalid commit -q --allow-empty -m init

# 設定が読めないと拒否を期待する項目が無意味に成功するので、最初に確認する。
if ! codex -p agent-harness mcp list --json >/dev/null 2>&1; then
  echo "FAIL プロファイルを読み込めません"; exit 1
fi

failed=0
sandbox() { codex -p agent-harness sandbox -P agent-harness -C "$work" -- "$@"; }
expect_ok() {
  local name="$1"; shift
  if sandbox "$@" >/dev/null 2>&1; then echo "PASS $name"; else echo "FAIL $name"; failed=1; fi
}
expect_denied() {
  local name="$1"; shift
  if sandbox "$@" >/dev/null 2>&1; then echo "FAIL $name"; failed=1; else echo "PASS $name"; fi
}

expect_ok     "作業用 clone への書き込み" sh -c 'echo x > file.txt'
expect_denied "HOME への書き込み"         sh -c 'echo x > "$HOME/.agent-harness-probe-file"'
expect_ok     "許可ドメインへの接続"       curl -sS -m 10 -o /dev/null https://registry.npmjs.org/
expect_denied "GitHub API への接続"       curl -sS -m 10 -o /dev/null https://api.github.com/
expect_denied "github.com への接続"       curl -sS -m 10 -o /dev/null https://github.com/
expect_denied "無関係なドメインへの接続"   curl -sS -m 10 -o /dev/null https://example.com/
# 対象が元々読めないと、許可された設定でも拒否と区別できない。
expect_read_denied() {
  local name="$1" path="$2"
  if [ ! -r "$path" ]; then echo "FAIL $name: 対象を読めないので確認できません ($path)"; failed=1; return; fi
  expect_denied "$name" sh -c "ls -d -- \"\$1\"/. >/dev/null 2>&1 && ls -- \"\$1\" >/dev/null || cat -- \"\$1\" >/dev/null" sh "$path"
}
expect_read_denied "gh の認証情報の読み取り" "$HOME/.config/gh/hosts.yml"
expect_read_denied "SSH の読み取り"          "$HOME/.ssh"
expect_read_denied "Codex の認証の読み取り"  "$HOME/.codex/auth.json"
expect_read_denied "Codex の設定の読み取り"  "$HOME/.codex/config.toml"
# 環境によって存在しないものは、存在するときだけ確認する。
for path in .codex/sessions .codex/history.jsonl .npmrc .netrc .git-credentials .pypirc .docker .kube .gnupg .config/gcloud; do
  if [ -e "$HOME/$path" ]; then expect_read_denied "~/$path の読み取り" "$HOME/$path"; fi
done

# API キー専用の Codex ホームは状態ディレクトリの下にある。試験用のファイルで確認する。
state="$HOME/.local/state/agent-harness"
mkdir -p "$state"
probe_file="$(mktemp "$state/.probe.XXXXXX")"
expect_read_denied "ハーネスの状態の読み取り" "$probe_file"
rm -f "$probe_file"
expect_denied ".git/hooks への書き込み"   sh -c 'echo x > .git/hooks/pre-commit'
expect_denied ".git/config への書き込み"  sh -c 'echo x >> .git/config'

# 試験専用のダミー値でキーチェーンの読み取りを確認する。実際の認証情報には触れない。
if security add-generic-password -U -s agent-harness-probe -a probe -w dummy >/dev/null 2>&1 &&
  security find-generic-password -s agent-harness-probe -w >/dev/null 2>&1; then
  expect_denied "キーチェーンの読み取り" security find-generic-password -s agent-harness-probe -w
else
  echo "FAIL キーチェーンの読み取り: 試験用の項目を用意できません"; failed=1
fi
security delete-generic-password -s agent-harness-probe >/dev/null 2>&1

expect_ok "一時オブジェクトでの差分出力" sh -c '
  mkdir -p .agent-run/objects &&
  GIT_INDEX_FILE=.agent-run/index \
  GIT_OBJECT_DIRECTORY="$PWD/.agent-run/objects" \
  GIT_ALTERNATE_OBJECT_DIRECTORIES="$PWD/.git/objects" \
  sh -c "git read-tree HEAD && git add -A && git diff --cached --binary --no-renames HEAD >/dev/null"'

expect_denied ".codex/config.toml への書き込み" sh -c 'mkdir -p .codex && echo x > .codex/config.toml'
expect_denied ".codex/hooks.json への書き込み"  sh -c 'mkdir -p .codex && echo x > .codex/hooks.json'

# 起動時に -c で無効化できることを確認する。ハーネスも同じ方法で全サーバーを止める。
if ! servers="$(codex -p agent-harness mcp list --json 2>/dev/null)"; then
  echo "FAIL MCP の一覧を取得できません"; failed=1
else
  overrides=()
  while IFS= read -r name; do
    [ -n "$name" ] && overrides+=(-c "mcp_servers.$name.enabled=false")
  done < <(printf '%s' "$servers" | python3 -c 'import json, sys; [print(s["name"]) for s in json.load(sys.stdin) if s.get("enabled")]')
  # macOS の bash 3.2 は set -u で空の配列を展開できない。
  remaining="$(codex -p agent-harness ${overrides[@]+"${overrides[@]}"} mcp list --json 2>/dev/null | python3 -c 'import json, sys; print(sum(1 for s in json.load(sys.stdin) if s.get("enabled")))')"
  if [ "$remaining" = "0" ]; then echo "PASS MCP の無効化"; else echo "FAIL MCP の無効化"; failed=1; fi
fi

exit "$failed"
