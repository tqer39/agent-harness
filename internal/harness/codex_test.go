//go:build !windows

package harness

import (
	"context"
	"testing"
)

func TestUpdateCodexUpgradesOutdatedCLI(t *testing.T) {
	outdated := `{"npm:@openai/codex":{"name":"npm:@openai/codex","requested":"latest","current":"0.148.0","latest":"0.167.0"}}`
	runner := &scriptRunner{responses: map[string]response{"mise outdated": {out: outdated}}}
	env := []string{"MISE_CONFIG_FILE=/c.toml"}
	from, to, err := UpdateCodex(context.Background(), runner, "mise", env)
	if err != nil || from != "0.148.0" || to != "0.167.0" {
		t.Fatalf("from=%s to=%s err=%v", from, to, err)
	}
	if !runner.called("mise", "upgrade", CodexTool) {
		t.Fatalf("calls=%+v", runner.calls)
	}
	// 設定ファイルを渡さないと、mise は利用者の Codex を管理外と判定する。
	for _, call := range runner.calls {
		if len(call.Env) != 1 || call.Env[0] != env[0] {
			t.Fatalf("env was not passed: %+v", call)
		}
	}
}

func TestUpdateCodexSkipsCurrentCLI(t *testing.T) {
	runner := &scriptRunner{responses: map[string]response{"mise outdated": {out: "{}\n"}}}
	from, to, err := UpdateCodex(context.Background(), runner, "mise", nil)
	if err != nil || from != "" || to != "" || len(runner.calls) != 1 {
		t.Fatalf("from=%s to=%s err=%v calls=%d", from, to, err, len(runner.calls))
	}
}

func TestUpdateCodexReportsFailure(t *testing.T) {
	runner := &scriptRunner{responses: map[string]response{
		"mise outdated": {out: `{"npm:@openai/codex":{"current":"0.148.0","latest":"0.167.0"}}`},
		"mise upgrade":  {err: &RunError{Name: "mise", Code: 1}},
	}}
	if _, _, err := UpdateCodex(context.Background(), runner, "mise", nil); err == nil {
		t.Fatal("a failed upgrade was ignored")
	}
	runner = &scriptRunner{responses: map[string]response{"mise outdated": {out: "warn: x"}}}
	if _, _, err := UpdateCodex(context.Background(), runner, "mise", nil); err == nil {
		t.Fatal("broken output was accepted")
	}
}
