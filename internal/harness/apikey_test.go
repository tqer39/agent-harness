//go:build !windows

package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupAPIKeyPipesKeyOverStdinOnly(t *testing.T) {
	const key = "sk-test-value"
	home := filepath.Join(t.TempDir(), "codex-api-home")
	runner := &scriptRunner{responses: map[string]response{}}
	secrets := fakeSecrets{OpenAIKeyRef: key}
	if err := SetupAPIKey(context.Background(), runner, secrets, "codex", home, []byte("profile\n")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(home)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("home mode=%v err=%v", info, err)
	}
	config, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil || !strings.Contains(string(config), `forced_login_method = "api"`) || !strings.Contains(string(config), `cli_auth_credentials_store = "file"`) {
		t.Fatalf("config=%s err=%v", config, err)
	}
	if profile, err := os.ReadFile(filepath.Join(home, "agent-harness.config.toml")); err != nil || string(profile) != "profile\n" {
		t.Fatalf("profile=%s err=%v", profile, err)
	}
	call := runner.calls[0]
	if call.Stdin != key || call.Secrets[0] != key || call.Env[0] != "CODEX_HOME="+home {
		t.Fatalf("call=%+v", call)
	}
	if strings.Contains(strings.Join(call.Args, " "), key) || strings.Contains(strings.Join(call.Env, " "), key) {
		t.Fatalf("key leaked into args or env: %+v", call)
	}
	if strings.Join(call.Args, " ") != "login --with-api-key" {
		t.Fatalf("args=%v", call.Args)
	}
}

func TestSetupAPIKeyRejectsEmptyKey(t *testing.T) {
	runner := &scriptRunner{responses: map[string]response{}}
	err := SetupAPIKey(context.Background(), runner, fakeSecrets{}, "codex", filepath.Join(t.TempDir(), "h"), nil)
	if err == nil || len(runner.calls) != 0 {
		t.Fatalf("err=%v calls=%d", err, len(runner.calls))
	}
}

func TestAPIKeyLoggedIn(t *testing.T) {
	runner := &scriptRunner{responses: map[string]response{"codex login": {out: "Logged in using an API key - sk-***\n"}}}
	if err := APIKeyLoggedIn(context.Background(), runner, "codex", "/h"); err != nil {
		t.Fatal(err)
	}
	if runner.calls[0].Env[0] != "CODEX_HOME=/h" || !runner.calls[0].CombineOutput {
		t.Fatalf("call=%+v", runner.calls[0])
	}
	runner = &scriptRunner{responses: map[string]response{"codex login": {out: "Logged in using ChatGPT\n"}}}
	if err := APIKeyLoggedIn(context.Background(), runner, "codex", "/h"); err == nil {
		t.Fatal("ChatGPT login must not count as API key login")
	}
	// 未ログインの案内にも "API key" が含まれることがある。
	runner = &scriptRunner{responses: map[string]response{"codex login": {out: "Not logged in. Run codex login --with-api-key to use an API key\n"}}}
	if err := APIKeyLoggedIn(context.Background(), runner, "codex", "/h"); err == nil {
		t.Fatal("a hint mentioning an API key must not count as login")
	}
	runner = &scriptRunner{responses: map[string]response{"codex login": {err: &RunError{Name: "codex", Code: 1, Stderr: "Logged in using an API key - sk-***\n"}}}}
	if err := APIKeyLoggedIn(context.Background(), runner, "codex", "/h"); err == nil {
		t.Fatal("a failed status command must not count as login")
	}
}

func TestAPIKeyProfileCurrent(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "agent-harness.config.toml"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := APIKeyProfileCurrent(home, []byte("new\n"), "/w"); err == nil || !strings.Contains(err.Error(), "setup-api-key") {
		t.Fatalf("err=%v", err)
	}
	if err := APIKeyProfileCurrent(home, []byte("old\n"), "/w"); err != nil {
		t.Fatal(err)
	}
	// Codex が作業用 clone の信頼設定を書き足しても、古いとは判定しない。
	trusted := "old\n\n[projects.\"/w/n-worktrees/agent-20261108-053323-f956\"]\ntrust_level = \"trusted\"\n"
	if err := os.WriteFile(filepath.Join(home, "agent-harness.config.toml"), []byte(trusted), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := APIKeyProfileCurrent(home, []byte("old\n"), "/w"); err != nil {
		t.Fatalf("trust for a harness clone was treated as stale: %v", err)
	}
	if err := APIKeyProfileCurrent(home, []byte("old\n"), "/other"); err == nil {
		t.Fatal("trust outside the work root was ignored")
	}
}
