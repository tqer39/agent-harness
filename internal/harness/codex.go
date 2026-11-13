//go:build !windows

package harness

import (
	"context"
	"encoding/json"
	"fmt"
)

// CodexTool は mise で Codex CLI を管理するときのツール名である。
const CodexTool = "npm:@openai/codex"

// UpdateCodex は mise の Codex CLI が古ければ更新し、更新した版を返す。
// 古い CLI は新しいモデルを一覧に含めず、実装担当が model_not_found で止まる。
// mise が管理していなければ何もしない。env は mise の設定ファイルを指す環境変数である。
func UpdateCodex(ctx context.Context, run Runner, mise string, env []string) (from, to string, err error) {
	out, err := run.Run(ctx, Cmd{Name: mise, Args: []string{"outdated", "--json", CodexTool}, Env: env})
	if err != nil {
		return "", "", fmt.Errorf("Codex CLI の版を確認できません: %w", err)
	}
	var outdated map[string]struct {
		Current string `json:"current"`
		Latest  string `json:"latest"`
	}
	if err := json.Unmarshal([]byte(out), &outdated); err != nil {
		return "", "", fmt.Errorf("mise outdated の出力を解釈できません: %w", err)
	}
	tool, ok := outdated[CodexTool]
	if !ok {
		return "", "", nil
	}
	if _, err := run.Run(ctx, Cmd{Name: mise, Args: []string{"upgrade", CodexTool}, Env: env}); err != nil {
		return "", "", fmt.Errorf("Codex CLI を %s から %s に更新できません: %w", tool.Current, tool.Latest, err)
	}
	return tool.Current, tool.Latest, nil
}
