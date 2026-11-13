//go:build !windows

package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func herdrFor(runner Runner) *Herdr {
	return &Herdr{Run: runner, HC: "hc", Codex: "/abs/codex", Claude: "/abs/claude", Poll: time.Millisecond}
}

func TestStartImplementerUsesProfileWithoutSecrets(t *testing.T) {
	runner := &scriptRunner{responses: map[string]response{
		"/abs/codex -p": {out: `[{"name":"context7","enabled":true},{"name":"off","enabled":false}]`},
	}}
	rec := &Record{RunID: "r1", Model: "gpt-6.1-sol", Effort: "high"}
	if err := herdrFor(runner).StartImplementer(context.Background(), rec, "/work", "task"); err != nil {
		t.Fatal(err)
	}
	call := runner.calls[1]
	joined := strings.Join(call.Args, " ")
	if strings.Contains(joined, "mcp_servers.off") {
		t.Errorf("disabled server was passed: %s", joined)
	}
	for _, part := range []string{"mcp_servers.context7.enabled=false", "--bg", "--name impl-r1", "--cwd /work", "--no-parent", "/abs/codex", "-p agent-harness", "-m gpt-6.1-sol", `model_reasoning_effort="high"`, `projects."/work".trust_level="trusted"`, "--ask-for-approval never"} {
		if !strings.Contains(joined, part) {
			t.Errorf("missing %q in %s", part, joined)
		}
	}
	if len(call.Env) != 0 {
		t.Fatalf("env must be empty: %v", call.Env)
	}
}

func TestWaitImplementerFindsResult(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".agent-run"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".agent-run", "result.json"), []byte(`{"run_id":"r1","round":0,"status":"done","summary":"s"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &scriptRunner{responses: map[string]response{}}
	outcome, result, err := herdrFor(runner).WaitImplementer(context.Background(), &Record{RunID: "r1"}, dir, time.Minute)
	if err != nil || outcome != ImplDone || result.Summary != "s" {
		t.Fatalf("outcome=%v result=%+v err=%v", outcome, result, err)
	}
}

func TestWaitImplementerDetectsQuotaFromScreen(t *testing.T) {
	runner := &scriptRunner{responses: map[string]response{
		"hc wait": {out: `{"ended":false,"reason":"timeout","status":null}`},
		"hc read": {out: "■ You've hit your usage limit. Visit settings\n"},
	}}
	outcome, _, err := herdrFor(runner).WaitImplementer(context.Background(), &Record{RunID: "r1"}, t.TempDir(), time.Minute)
	if err != nil || outcome != ImplQuota {
		t.Fatalf("outcome=%v err=%v", outcome, err)
	}
}

func TestWaitImplementerDetectsTrustPromptAndExit(t *testing.T) {
	runner := &scriptRunner{responses: map[string]response{
		"hc wait": {out: `{"ended":false,"reason":"timeout"}`},
		"hc read": {out: "Do you trust the contents of this directory?\n"},
	}}
	outcome, _, err := herdrFor(runner).WaitImplementer(context.Background(), &Record{RunID: "r1"}, t.TempDir(), time.Minute)
	if err != nil || outcome != ImplBlocked {
		t.Fatalf("trust: outcome=%v err=%v", outcome, err)
	}
	// 自分で作った clone なので 1 度だけ承認し、繰り返すなら止める。
	keys := 0
	for _, call := range runner.calls {
		if len(call.Args) > 0 && call.Args[0] == "keys" {
			keys++
			if strings.Join(call.Args, " ") != "keys impl-r1 enter" {
				t.Fatalf("args=%v", call.Args)
			}
		}
	}
	if keys != 1 {
		t.Fatalf("keys=%d", keys)
	}
	runner = &scriptRunner{responses: map[string]response{"hc wait": {out: `{"ended":true,"exit_code":1}`}}}
	outcome, _, err = herdrFor(runner).WaitImplementer(context.Background(), &Record{RunID: "r1"}, t.TempDir(), time.Minute)
	if err != nil || outcome != ImplExited {
		t.Fatalf("exit: outcome=%v err=%v", outcome, err)
	}
}

// stepRunner は hc の呼び出しごとに応答を変える。
type stepRunner struct {
	wait func() string
	read func() string
}

func (r *stepRunner) Run(_ context.Context, c Cmd) (string, error) {
	switch {
	case len(c.Args) > 0 && c.Args[0] == "wait":
		return r.wait(), nil
	case len(c.Args) > 0 && c.Args[0] == "read":
		return r.read(), nil
	}
	return "", nil
}

func TestWaitImplementerReadsResultWrittenBeforeExit(t *testing.T) {
	work := t.TempDir()
	runner := &stepRunner{wait: func() string {
		// 待機中に結果を書いて終了した。
		if err := os.MkdirAll(filepath.Join(work, ".agent-run"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(work, ".agent-run", "result.json"), []byte(`{"run_id":"r1","round":0,"status":"done","summary":"s"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		return `{"ended":true,"exit_code":0}`
	}}
	outcome, result, err := herdrFor(runner).WaitImplementer(context.Background(), &Record{RunID: "r1"}, work, time.Minute)
	if err != nil || outcome != ImplDone || result.Summary != "s" {
		t.Fatalf("outcome=%v result=%+v err=%v", outcome, result, err)
	}
}

func TestWaitImplementerIgnoresQuotaWordsWhileWorking(t *testing.T) {
	// レート制限を実装するタスクでは、作業中の画面に同じ語が出る。画面が動いていれば利用上限ではない。
	work := t.TempDir()
	reads := 0
	runner := &stepRunner{
		wait: func() string { return `{"ended":false,"reason":"timeout"}` },
		read: func() string {
			reads++
			if reads == 3 {
				if err := os.MkdirAll(filepath.Join(work, ".agent-run"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(work, ".agent-run", "result.json"), []byte(`{"run_id":"r1","round":0,"status":"done","summary":"s"}`), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			return fmt.Sprintf("add test: rate limit reached returns 429 (%ds)\n", reads)
		},
	}
	outcome, _, err := herdrFor(runner).WaitImplementer(context.Background(), &Record{RunID: "r1"}, work, time.Minute)
	if err != nil || outcome != ImplDone {
		t.Fatalf("outcome=%v err=%v", outcome, err)
	}
}

func TestWaitImplementerTimesOut(t *testing.T) {
	runner := &scriptRunner{responses: map[string]response{
		"hc wait": {out: `{"ended":false,"reason":"timeout"}`},
		"hc read": {out: "working\n"},
	}}
	outcome, _, err := herdrFor(runner).WaitImplementer(context.Background(), &Record{RunID: "r1"}, t.TempDir(), 0)
	if err != nil || outcome != ImplTimeout {
		t.Fatalf("outcome=%v err=%v", outcome, err)
	}
}

func TestReviewArgumentsAreReadOnly(t *testing.T) {
	args := reviewArgs("/abs/claude", "claude-opus-5-5", "prompt")
	joined := strings.Join(args, " ")
	if !slices.Contains(args, "Read,Grep,Glob") || !strings.Contains(joined, "--strict-mcp-config") || !strings.Contains(joined, "-p") {
		t.Fatalf("args=%v", args)
	}
	for _, forbidden := range []string{"Bash", "Write", "Edit", "WebFetch", "bypassPermissions"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("%s must not be allowed: %s", forbidden, joined)
		}
	}
}

func TestReviewReadsVerdictFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".agent-run"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".agent-run", "verdict.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &scriptRunner{responses: map[string]response{"hc wait": {out: `{"ended":true,"exit_code":0}`}}}
	outcome, data, err := herdrFor(runner).Review(context.Background(), &Record{RunID: "r1", ReviewerModel: "m"}, dir, "p", time.Minute)
	if err != nil || outcome != ReviewDone || string(data) != "{}" {
		t.Fatalf("outcome=%v data=%q err=%v", outcome, data, err)
	}
}

func TestReviewDetectsQuota(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".agent-run"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".agent-run", "verdict.err"), []byte("Claude usage limit reached"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &scriptRunner{responses: map[string]response{"hc wait": {out: `{"ended":true,"exit_code":1}`}}}
	outcome, _, err := herdrFor(runner).Review(context.Background(), &Record{RunID: "r1", ReviewerModel: "m"}, dir, "p", time.Minute)
	if err != nil || outcome != ReviewQuota {
		t.Fatalf("outcome=%v err=%v", outcome, err)
	}
}

func TestStartImplementerUsesDedicatedCodexHome(t *testing.T) {
	runner := &scriptRunner{responses: map[string]response{"/abs/codex -p": {out: `[]`}}}
	herdr := herdrFor(runner)
	herdr.CodexHome = "/state/codex-api-home"
	rec := &Record{RunID: "r1", Model: "m", Effort: "high"}
	if err := herdr.StartImplementer(context.Background(), rec, "/work", "task"); err != nil {
		t.Fatal(err)
	}
	if runner.calls[0].Env[0] != "CODEX_HOME=/state/codex-api-home" {
		t.Fatalf("mcp list env=%v", runner.calls[0].Env)
	}
	joined := strings.Join(runner.calls[1].Args, " ")
	if !strings.Contains(joined, " CODEX_HOME=/state/codex-api-home /abs/codex -p agent-harness") {
		t.Fatalf("args=%s", joined)
	}
}

func TestStartImplementerGetsVerifyEnvironment(t *testing.T) {
	// 実装担当のコマンドも同じ権限で動く。HOME の下に書けないので、依存の取得先を作業用 clone に向ける。
	runner := &scriptRunner{responses: map[string]response{"/abs/codex -p": {out: `[]`}}}
	if err := herdrFor(runner).StartImplementer(context.Background(), &Record{RunID: "r1"}, "/work", "task"); err != nil {
		t.Fatal(err)
	}
	// hc は引数ごとに渡すので、結合した文字列ではなく要素の境界も比較する。
	args := runner.calls[1].Args
	start := slices.Index(args, "--")
	want := append([]string{"--", "/usr/bin/env"}, sandboxEnv("/work")...)
	want = append(want, "/abs/codex")
	if start < 0 || len(args)-start < len(want) || !slices.Equal(args[start:start+len(want)], want) {
		t.Fatalf("args=%q", args)
	}
}

func TestStartImplementerKeepsDefaultHome(t *testing.T) {
	runner := &scriptRunner{responses: map[string]response{"/abs/codex -p": {out: `[]`}}}
	if err := herdrFor(runner).StartImplementer(context.Background(), &Record{RunID: "r1"}, "/work", "task"); err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(runner.calls[1].Args, " "); strings.Contains(joined, "CODEX_HOME") {
		t.Fatalf("args=%s", joined)
	}
}

func TestWaitImplementerDetectsAPIBudgetExhaustion(t *testing.T) {
	for _, screen := range []string{"insufficient_quota", "You exceeded your current quota, please check your plan"} {
		runner := &scriptRunner{responses: map[string]response{
			"hc wait": {out: `{"ended":false,"reason":"timeout"}`},
			"hc read": {out: screen},
		}}
		outcome, _, err := herdrFor(runner).WaitImplementer(context.Background(), &Record{RunID: "r1"}, t.TempDir(), time.Minute)
		if err != nil || outcome != ImplQuota {
			t.Errorf("%q: outcome=%v err=%v", screen, outcome, err)
		}
	}
}

func TestReviewCanReadOwnDirectoryButNotAPIKey(t *testing.T) {
	joined := strings.Join(reviewArgs("/abs/claude", "m", "p"), " ")
	if strings.Contains(joined, "Read(~/.local/state/agent-harness/**)") {
		t.Fatalf("the review directory must stay readable: %s", joined)
	}
	if !strings.Contains(joined, "Read(~/.local/state/agent-harness/codex-api-home/**)") {
		t.Fatalf("the API key must stay denied: %s", joined)
	}
}

func TestQuotaPatternIgnoresAvailableResetNotice(t *testing.T) {
	// Codex は上限に達していなくても、残りのリセット回数を常に表示する。
	for _, screen := range []string{
		"You have 1 usage limit reset available",
		"■ The 'gpt-6.1-sol' model is not supported when using Codex with a ChatGPT account.\nYou have 1 usage limit reset available",
	} {
		if quotaPattern.MatchString(screen) {
			t.Errorf("%q was treated as quota exhaustion", screen)
		}
	}
	for _, screen := range []string{
		"■ You've hit your usage limit. Visit settings",
		"■ You’ve hit your usage limit. Visit https://chatgpt.com/settings/usage",
		"You've hit your limit · resets 3pm",
		"Claude usage limit reached",
	} {
		if !quotaPattern.MatchString(screen) {
			t.Errorf("%q was not treated as quota exhaustion", screen)
		}
	}
}
