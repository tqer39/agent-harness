//go:build !windows

package harness

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestModelAvailable(t *testing.T) {
	catalog := `{"models":[{"slug":"gpt-5.6-sol","visibility":"list"},{"slug":"gpt-reserve","visibility":"hide"}]}`
	runner := &scriptRunner{responses: map[string]response{"codex debug": {out: catalog}}}
	if err := ModelAvailable(context.Background(), runner, "codex", "/api-home", "gpt-5.6-sol"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(runner.calls[0].Env, "CODEX_HOME=/api-home") {
		t.Fatalf("the catalog must come from the same auth: %v", runner.calls[0].Env)
	}
	// ChatGPT 認証の古い CLI は gpt-6.1-sol を一覧に含めず、実行すると model_not_found になる。
	err := ModelAvailable(context.Background(), runner, "codex", "", "gpt-6.1-sol")
	if err == nil || !strings.Contains(err.Error(), "gpt-5.6-sol") {
		t.Fatalf("err=%v", err)
	}
	if runner.calls[1].Env != nil {
		t.Fatalf("default home must not set CODEX_HOME: %v", runner.calls[1].Env)
	}
}

func TestModelAvailableRejectsBrokenCatalog(t *testing.T) {
	runner := &scriptRunner{responses: map[string]response{"codex debug": {out: "not json"}}}
	if err := ModelAvailable(context.Background(), runner, "codex", "", "m"); err == nil {
		t.Fatal("broken catalog was accepted")
	}
}
