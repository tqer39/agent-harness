//go:build !windows

package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRunRequiresApprovalAndCriteria(t *testing.T) {
	var out bytes.Buffer
	err := dispatch([]string{"run", "--repo", "o/n", "--title", "t", "--task-file", "/nonexistent", "--criteria-file", "/nonexistent"}, &out)
	if err == nil || !strings.Contains(err.Error(), "approved-by-human") {
		t.Fatalf("err=%v", err)
	}
}

func TestRepoIsRequired(t *testing.T) {
	// 空の名前で停止の解除やトークンの発行に進まないよう、実際の状態ディレクトリから離す。
	t.Setenv("HOME", t.TempDir())
	for _, command := range []string{"reconcile", "clear-stop"} {
		var out bytes.Buffer
		if err := dispatch([]string{command}, &out); err == nil || !strings.Contains(err.Error(), "--repo") {
			t.Errorf("%s: err=%v", command, err)
		}
	}
}

func TestUnknownCommand(t *testing.T) {
	var out bytes.Buffer
	if err := dispatch([]string{"unknown"}, &out); err == nil {
		t.Fatal("unknown command was accepted")
	}
}

func TestProfileOnlyAllowsCodexImplementer(t *testing.T) {
	if _, err := profile("claude", "m", "medium"); err == nil {
		t.Fatal("claude implementer must stay disabled until isolated")
	}
	p, err := profile("codex", "gpt-6.1-sol", "high")
	if err != nil || p.Reviewer == "" {
		t.Fatalf("profile=%+v err=%v", p, err)
	}
}

func TestProfileRejectsInvalidEffort(t *testing.T) {
	if _, err := profile("codex", "gpt-6.1-sol", "extreme"); err == nil {
		t.Fatal("invalid effort was accepted")
	}
}

func TestSetupProfileRefusesToOverwriteDifferentFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if err := installProfile(); err != nil {
		t.Fatal(err)
	}
	if err := installProfile(); err != nil {
		t.Fatalf("identical file: %v", err)
	}
	if err := writeForTest(dir + "/.codex/agent-harness.config.toml"); err != nil {
		t.Fatal(err)
	}
	if err := installProfile(); err == nil {
		t.Fatal("different file was overwritten")
	}
}

func writeForTest(path string) error {
	return os.WriteFile(path, []byte("changed\n"), 0o600)
}

func TestClaudeLoggedIn(t *testing.T) {
	if err := claudeLoggedIn(`{"loggedIn":true}`); err != nil {
		t.Fatal(err)
	}
	if err := claudeLoggedIn(`{"loggedIn":false}`); err == nil {
		t.Fatal("logged out state was accepted")
	}
	if err := claudeLoggedIn(`not json`); err == nil {
		t.Fatal("invalid output was accepted")
	}
}

func TestCodexHomeForAuth(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	if home, err := codexHomeFor(""); err != nil || home != "" {
		t.Fatalf("home=%q err=%v", home, err)
	}
	if home, err := codexHomeFor("chatgpt"); err != nil || home != "" {
		t.Fatalf("home=%q err=%v", home, err)
	}
	home, err := codexHomeFor("api-key")
	if err != nil || home != "/home/u/.local/state/agent-harness/codex-api-home" {
		t.Fatalf("home=%q err=%v", home, err)
	}
	if _, err := codexHomeFor("other"); err == nil {
		t.Fatal("unknown auth was accepted")
	}
}

func TestRealPathResolvesMiseShims(t *testing.T) {
	which := func(name string) (string, error) { return "/installs/" + name + "/1/bin/" + name, nil }
	got, err := realPath("/home/u/.local/share/mise/shims/claude", "claude", which)
	if err != nil || got != "/installs/claude/1/bin/claude" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	got, err = realPath("/opt/homebrew/bin/gh", "gh", which)
	if err != nil || got != "/opt/homebrew/bin/gh" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	failing := func(string) (string, error) { return "", errors.New("not active") }
	if _, err := realPath("/home/u/.local/share/mise/shims/claude", "claude", failing); err == nil {
		t.Fatal("an unresolved shim must fail instead of falling back to another version")
	}
}

func TestCommandOutputStopsAtLimit(t *testing.T) {
	// sh を止めても sleep が出力を開いたまま残る。読み取りの待機にも上限がなければ 5 秒待つ。
	start := time.Now()
	_, err := commandOutput(200*time.Millisecond, "sh", "-c", "sleep 5; true")
	if err == nil || !strings.Contains(err.Error(), "以内に終わりません") {
		t.Fatalf("err=%v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("elapsed=%v", elapsed)
	}
}

func TestCommandOutputReturnsPath(t *testing.T) {
	got, err := commandOutput(5*time.Second, "sh", "-c", "echo /x/bin/tool")
	if err != nil || got != "/x/bin/tool" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	// 正常に終わった後も、子の子が stderr を開いたまま残ることがある。出力は使う。
	got, err = commandOutput(5*time.Second, "sh", "-c", "echo /x/bin/tool; sleep 3 >/dev/null &")
	if err != nil || got != "/x/bin/tool" {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestSetupProfileAcceptsTrustAddedByCodex(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if err := installProfile(); err != nil {
		t.Fatal(err)
	}
	path := dir + "/.codex/agent-harness.config.toml"
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString("\n[projects.\"" + dir + "/workspace/tqer39/n-worktrees/agent-20261108-053323-f956\"]\ntrust_level = \"trusted\"\n")
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := installProfile(); err != nil {
		t.Fatalf("doctor fails after Codex trusted a harness clone: %v", err)
	}
}

func TestRunStopsBeforeCloningWhenModelIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	// ChatGPT 認証の古い CLI と同じく、既定のモデルを一覧に含めない codex を置く。
	codex := dir + "/codex"
	script := "#!/bin/sh\necho '{\"models\":[{\"slug\":\"gpt-5.6-sol\"}]}'\n"
	if err := os.WriteFile(codex, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/mise", []byte("#!/bin/sh\necho '{}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	task := dir + "/task.md"
	if err := os.WriteFile(task, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := dispatch([]string{"run", "--repo", "o/n", "--title", "t", "--task-file", task, "--criteria-file", task, "--approved-by-human"}, &out)
	if err == nil || !strings.Contains(err.Error(), defaultModel) || !strings.Contains(err.Error(), "gpt-5.6-sol") {
		t.Fatalf("err=%v", err)
	}
	if _, statErr := os.Stat(dir + "/.local/state/agent-harness"); !os.IsNotExist(statErr) {
		t.Fatalf("run prepared state before checking the model: %v", statErr)
	}
}

func TestRunUpdatesCodexBeforeCheckingModel(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("MISE_CONFIG_FILE", dir+"/config.toml")
	t.Setenv("MISE_DATA_DIR", dir+"/mise-data")
	log := dir + "/calls.log"
	// 呼び出しの順を記録する。mise は利用者の設定とインストール先を受け取ったときだけ Codex を管理する。
	mise := "#!/bin/sh\n" +
		"echo \"mise $1\" >> " + log + "\n" +
		"[ \"$MISE_CONFIG_FILE\" = \"" + dir + "/config.toml\" ] && [ \"$MISE_DATA_DIR\" = \"" + dir + "/mise-data\" ] || { echo '{}'; exit 0; }\n" +
		"[ \"$1\" = outdated ] && echo '{\"npm:@openai/codex\":{\"current\":\"0.148.0\",\"latest\":\"0.167.0\"}}'\n" +
		"exit 0\n"
	codex := "#!/bin/sh\n" +
		"echo \"codex $1\" >> " + log + "\n" +
		"echo '{\"models\":[{\"slug\":\"" + defaultModel + "\"}]}'\n"
	for name, script := range map[string]string{"mise": mise, "codex": codex} {
		if err := os.WriteFile(dir+"/"+name, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// claude などを置かず、build の実行ファイルの解決で確実に止める。
	t.Setenv("PATH", dir)
	task := dir + "/task.md"
	if err := os.WriteFile(task, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := dispatch([]string{"run", "--repo", "o/n", "--title", "t", "--task-file", task, "--criteria-file", task, "--approved-by-human"}, &out)
	if err == nil || !strings.Contains(err.Error(), "claude") {
		t.Fatalf("run must stop at build: %v", err)
	}
	if !strings.Contains(out.String(), "0.148.0 から 0.167.0 に更新") {
		t.Fatalf("out=%s", out.String())
	}
	calls, readErr := os.ReadFile(log)
	if readErr != nil || string(calls) != "mise outdated\nmise upgrade\ncodex debug\n" {
		t.Fatalf("calls=%q err=%v", calls, readErr)
	}
}

func TestRunContinuesWhenCodexUpdateFails(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	scripts := map[string]string{
		"mise":  "#!/bin/sh\necho 'registry unreachable' >&2\nexit 1\n",
		"codex": "#!/bin/sh\necho '{\"models\":[{\"slug\":\"" + defaultModel + "\"}]}'\n",
	}
	for name, script := range scripts {
		if err := os.WriteFile(dir+"/"+name, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	task := dir + "/task.md"
	if err := os.WriteFile(task, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := dispatch([]string{"run", "--repo", "o/n", "--title", "t", "--task-file", task, "--criteria-file", task, "--approved-by-human"}, &out)
	// 今の CLI でモデルを使えるので、更新の失敗では止まらず build まで進む。
	if err == nil || !strings.Contains(err.Error(), "claude") || !strings.Contains(out.String(), "今の版で続けます") {
		t.Fatalf("out=%s err=%v", out.String(), err)
	}
}
