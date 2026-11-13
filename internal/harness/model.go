//go:build !windows

package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// ModelAvailable は Codex が実装担当のモデルを使えるかを確認する。
// 使えるモデルは認証方式と Codex CLI の版で変わり、使えないと実装担当は 400 model_not_found で止まる。
// codex debug models はアカウントと CLI の版に応じた一覧を返し、利用枠を消費しない。
func ModelAvailable(ctx context.Context, run Runner, codex, home, model string) error {
	var env []string
	if home != "" {
		env = []string{"CODEX_HOME=" + home}
	}
	out, err := run.Run(ctx, Cmd{Name: codex, Args: []string{"debug", "models"}, Env: env})
	if err != nil {
		return fmt.Errorf("Codex のモデル一覧を取得できません: %w", err)
	}
	var catalog struct {
		Models []struct {
			Slug string `json:"slug"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(out), &catalog); err != nil {
		return fmt.Errorf("Codex のモデル一覧を解釈できません: %w", err)
	}
	slugs := make([]string, 0, len(catalog.Models))
	for _, m := range catalog.Models {
		slugs = append(slugs, m.Slug)
	}
	if slices.Contains(slugs, model) {
		return nil
	}
	return fmt.Errorf("モデル %s はこの Codex の認証方式と CLI の版では使えません（使えるモデル: %s）。Codex CLI を更新するか、--model で一覧のモデルを指定してください", model, strings.Join(slugs, ", "))
}
