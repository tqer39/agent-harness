//go:build !windows

package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// resultFiles は実装担当が最後の応答として書く結果である。
func resultFiles(round int) map[string]string {
	return map[string]string{".agent-run/result.json": fmt.Sprintf(`{"run_id":"r1","round":%d,"status":"done","summary":"s"}`, round)}
}

const (
	codexMCP  = "/abs/codex -p agent-harness mcp"
	codexExec = "/abs/codex -p agent-harness exec"
	threadOK  = `{"type":"thread.started","thread_id":"t-1"}` + "\n" +
		`{"type":"item.completed","item":{"id":"i1","type":"command_execution","command":"go test ./...","exit_code":0}}` + "\n"
)

func codexFor(t *testing.T, runner Runner) (*CodexExec, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	return &CodexExec{Run: runner, Codex: "/abs/codex", Claude: "/abs/claude", Events: t.TempDir(), Out: &out}, &out
}

func writeResult(t *testing.T, work, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(work, ".agent-run"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".agent-run", "result.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func execCall(t *testing.T, runner *scriptRunner) Cmd {
	t.Helper()
	for _, call := range runner.calls {
		if slices.Contains(call.Args, "exec") {
			return call
		}
	}
	t.Fatalf("codex exec was not called: %+v", runner.calls)
	return Cmd{}
}

func TestImplementRunsCodexExecWithProfile(t *testing.T) {
	work := t.TempDir()
	runner := &scriptRunner{responses: map[string]response{
		codexMCP:  {out: `[{"name":"context7","enabled":true},{"name":"off","enabled":false}]`},
		codexExec: {stream: threadOK, files: resultFiles(0)},
	}}
	codex, out := codexFor(t, runner)
	rec := &Record{RunID: "r1", Model: "gpt-6.1-sol", Effort: "high"}
	report, err := codex.Implement(context.Background(), rec, work, "task", time.Minute)
	if err != nil || report.Outcome != ImplDone || report.Result.Summary != "s" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	call := execCall(t, runner)
	joined := strings.Join(call.Args, " ")
	for _, part := range []string{
		"-p agent-harness exec --json", "-m gpt-6.1-sol", `model_reasoning_effort="high"`,
		`projects."` + work + `".trust_level="trusted"`, "mcp_servers.context7.enabled=false",
		"--output-schema " + filepath.Join(work, ".agent-run", "result.schema.json"),
		"-o " + filepath.Join(work, ".agent-run", "result.json"),
	} {
		if !strings.Contains(joined, part) {
			t.Errorf("missing %q in %s", part, joined)
		}
	}
	if strings.Contains(joined, "mcp_servers.off") || strings.Contains(joined, "resume") {
		t.Errorf("unexpected args: %s", joined)
	}
	if call.Args[len(call.Args)-1] != "-" || call.Stdin != "task" || call.Dir != work {
		t.Errorf("prompt must go through stdin: args=%v stdin=%q dir=%s", call.Args, call.Stdin, call.Dir)
	}
	if !slices.Contains(call.Env, "MISE_STATE_DIR="+work+"/.agent-run/cache/mise-state") {
		t.Errorf("env=%v", call.Env)
	}
	for _, env := range call.Env {
		if strings.HasPrefix(env, "CODEX_HOME=") {
			t.Errorf("default home must be kept: %v", call.Env)
		}
	}
	schema, err := os.ReadFile(filepath.Join(work, ".agent-run", "result.schema.json"))
	if err != nil || string(schema) != ResultSchema {
		t.Errorf("schema=%q err=%v", schema, err)
	}
	logged, err := os.ReadFile(filepath.Join(codex.Events, "r1.events.jsonl"))
	if err != nil || string(logged) != threadOK {
		t.Errorf("events=%q err=%v", logged, err)
	}
	if rec.ThreadID != "t-1" || !strings.Contains(out.String(), "$ go test ./... (exit 0)") {
		t.Errorf("thread=%q out=%q", rec.ThreadID, out.String())
	}
}

func TestImplementResumesThread(t *testing.T) {
	work := t.TempDir()
	runner := &scriptRunner{responses: map[string]response{codexMCP: {out: `[]`}, codexExec: {files: resultFiles(1)}}}
	codex, _ := codexFor(t, runner)
	rec := &Record{RunID: "r1", Round: 1, Model: "m", Effort: "low", ThreadID: "t-1"}
	report, err := codex.Implement(context.Background(), rec, work, "fix", time.Minute)
	if err != nil || report.Outcome != ImplDone {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	args := execCall(t, runner).Args
	if !strings.Contains(strings.Join(args, " "), "exec resume --json") || args[len(args)-2] != "t-1" || args[len(args)-1] != "-" {
		t.Fatalf("args=%v", args)
	}
}

func TestImplementUsesDedicatedCodexHome(t *testing.T) {
	work := t.TempDir()
	runner := &scriptRunner{responses: map[string]response{codexMCP: {out: `[]`}, codexExec: {files: resultFiles(0)}}}
	codex, _ := codexFor(t, runner)
	codex.CodexHome = "/state/codex-api-home"
	if _, err := codex.Implement(context.Background(), &Record{RunID: "r1"}, work, "task", time.Minute); err != nil {
		t.Fatal(err)
	}
	for _, call := range runner.calls {
		if !slices.Contains(call.Env, "CODEX_HOME=/state/codex-api-home") {
			t.Errorf("%v: env=%v", call.Args, call.Env)
		}
	}
}

func TestImplementDetectsQuota(t *testing.T) {
	cases := map[string]response{
		"event":  {stream: `{"type":"turn.failed","error":{"message":"You've hit your usage limit. Try again later."}}`, err: &RunError{Code: 1}},
		"stderr": {err: &RunError{Code: 1, Stderr: "insufficient_quota"}},
	}
	for name, res := range cases {
		runner := &scriptRunner{responses: map[string]response{codexMCP: {out: `[]`}, codexExec: res}}
		codex, _ := codexFor(t, runner)
		report, err := codex.Implement(context.Background(), &Record{RunID: "r1"}, t.TempDir(), "task", time.Minute)
		if err != nil || report.Outcome != ImplQuota {
			t.Errorf("%s: report=%+v err=%v", name, report, err)
		}
	}
}

func TestImplementReportsUnknownFailure(t *testing.T) {
	runner := &scriptRunner{responses: map[string]response{
		codexMCP:  {out: `[]`},
		codexExec: {stream: `{"type":"error","message":"model is not supported"}` + "\n", err: &RunError{Code: 1}},
	}}
	codex, _ := codexFor(t, runner)
	report, err := codex.Implement(context.Background(), &Record{RunID: "r1"}, t.TempDir(), "task", time.Minute)
	if err != nil || report.Outcome != ImplFailed || report.Detail != "model is not supported" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestImplementReportsExitWithoutResult(t *testing.T) {
	runner := &scriptRunner{responses: map[string]response{
		codexMCP:  {out: `[]`},
		codexExec: {err: &RunError{Code: 2, Stderr: "boom"}},
	}}
	codex, _ := codexFor(t, runner)
	report, err := codex.Implement(context.Background(), &Record{RunID: "r1"}, t.TempDir(), "task", time.Minute)
	if err != nil || report.Outcome != ImplExited || report.Detail != "boom" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestImplementPrefersResultOverTransientError(t *testing.T) {
	work := t.TempDir()
	runner := &scriptRunner{responses: map[string]response{
		codexMCP:  {out: `[]`},
		codexExec: {stream: `{"type":"error","message":"Reconnecting... 1/5"}` + "\n", files: resultFiles(0)},
	}}
	codex, _ := codexFor(t, runner)
	report, err := codex.Implement(context.Background(), &Record{RunID: "r1"}, work, "task", time.Minute)
	if err != nil || report.Outcome != ImplDone {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestImplementTimesOut(t *testing.T) {
	codex, _ := codexFor(t, blockingRunner{listMCP: true})
	report, err := codex.Implement(context.Background(), &Record{RunID: "r1"}, t.TempDir(), "task", 20*time.Millisecond)
	if err != nil || report.Outcome != ImplTimeout {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

// MCP の一覧の取得が止まっても、ラウンドの時間上限で打ち切る。
func TestImplementBoundsMCPListByRoundLimit(t *testing.T) {
	codex, _ := codexFor(t, blockingRunner{})
	report, err := codex.Implement(context.Background(), &Record{RunID: "r1"}, t.TempDir(), "task", 20*time.Millisecond)
	if err != nil || report.Outcome != ImplTimeout {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

// 前の実行が残した同じ round の結果を、今回の結果と取り違えない。
func TestImplementRemovesPreviousResult(t *testing.T) {
	work := t.TempDir()
	writeResult(t, work, `{"run_id":"r1","round":0,"status":"done","summary":"old"}`)
	runner := &scriptRunner{responses: map[string]response{
		codexMCP:  {out: `[]`},
		codexExec: {err: &RunError{Code: 1, Stderr: "boom"}},
	}}
	codex, _ := codexFor(t, runner)
	report, err := codex.Implement(context.Background(), &Record{RunID: "r1"}, work, "task", time.Minute)
	if err != nil || report.Outcome != ImplExited {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

// 中断されても同じ thread で再開できるよう、thread.started を受け取った時点で記録する。
func TestImplementSavesThreadWhenItStarts(t *testing.T) {
	runner := &scriptRunner{responses: map[string]response{
		codexMCP:  {out: `[]`},
		codexExec: {stream: threadOK, err: &RunError{Code: 1}},
	}}
	codex, _ := codexFor(t, runner)
	var saved []string
	codex.Save = func(rec *Record) error {
		saved = append(saved, rec.ThreadID)
		return nil
	}
	rec := &Record{RunID: "r1"}
	if _, err := codex.Implement(context.Background(), rec, t.TempDir(), "task", time.Minute); err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 || saved[0] != "t-1" || rec.ThreadID != "t-1" {
		t.Fatalf("saved=%v thread=%q", saved, rec.ThreadID)
	}
}

func TestCodexReviewReturnsStdout(t *testing.T) {
	runner := &scriptRunner{responses: map[string]response{"/abs/claude": {out: "{}"}}}
	codex, out := codexFor(t, runner)
	outcome, data, err := codex.Review(context.Background(), &Record{RunID: "r1", ReviewerModel: "m"}, "/review", "p", time.Minute)
	if err != nil || outcome != ReviewDone || string(data) != "{}" {
		t.Fatalf("outcome=%v data=%q err=%v", outcome, data, err)
	}
	call := runner.calls[0]
	if call.Dir != "/review" || !slices.Contains(call.Args, "Read,Grep,Glob") || !strings.Contains(out.String(), "レビュー") {
		t.Fatalf("call=%+v out=%q", call, out.String())
	}
}

func TestCodexReviewDetectsQuota(t *testing.T) {
	runner := &scriptRunner{responses: map[string]response{"/abs/claude": {err: &RunError{Code: 1, Stderr: "Claude usage limit reached"}}}}
	codex, _ := codexFor(t, runner)
	outcome, _, err := codex.Review(context.Background(), &Record{RunID: "r1", ReviewerModel: "m"}, "/review", "p", time.Minute)
	if err != nil || outcome != ReviewQuota {
		t.Fatalf("outcome=%v err=%v", outcome, err)
	}
}

func TestCodexReviewFailureKeepsOutput(t *testing.T) {
	runner := &scriptRunner{responses: map[string]response{"/abs/claude": {err: &RunError{Code: 1, Stdout: `{"is_error":true}`}}}}
	codex, _ := codexFor(t, runner)
	outcome, data, err := codex.Review(context.Background(), &Record{RunID: "r1", ReviewerModel: "m"}, "/review", "p", time.Minute)
	if err != nil || outcome != ReviewFailed || string(data) != `{"is_error":true}` {
		t.Fatalf("outcome=%v data=%q err=%v", outcome, data, err)
	}
}

func TestCodexReviewTimesOut(t *testing.T) {
	codex, _ := codexFor(t, blockingRunner{})
	outcome, _, err := codex.Review(context.Background(), &Record{RunID: "r1", ReviewerModel: "m"}, "/review", "p", 20*time.Millisecond)
	if err != nil || outcome != ReviewTimeout {
		t.Fatalf("outcome=%v err=%v", outcome, err)
	}
}

// resultThenBlockRunner は結果を書いた後、キャンセルまで戻らない。
type resultThenBlockRunner struct{ round int }

func (r resultThenBlockRunner) Run(ctx context.Context, c Cmd) (string, error) {
	if strings.Contains(strings.Join(c.Args, " "), "mcp list") {
		return "[]", nil
	}
	for name, body := range resultFiles(r.round) {
		if err := os.WriteFile(filepath.Join(c.Dir, name), []byte(body), 0o600); err != nil {
			return "", err
		}
	}
	<-ctx.Done()
	return "", &RunError{Name: c.Name, Code: -1}
}

// 結果が書かれていれば、後始末で時間上限を越えても完了として扱う。
func TestImplementPrefersResultOverTimeout(t *testing.T) {
	codex, _ := codexFor(t, resultThenBlockRunner{})
	report, err := codex.Implement(context.Background(), &Record{RunID: "r1"}, t.TempDir(), "task", 50*time.Millisecond)
	if err != nil || report.Outcome != ImplDone {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

// 失敗の本文があるときは、stderr の一時的な 429 などで quota と誤判定しない。
func TestImplementIgnoresStderrWhenFailureHasBody(t *testing.T) {
	runner := &scriptRunner{responses: map[string]response{
		codexMCP:  {out: `[]`},
		codexExec: {stream: `{"type":"error","message":"model is not supported"}` + "\n", err: &RunError{Code: 1, Stderr: "rate limit reached"}},
	}}
	codex, _ := codexFor(t, runner)
	report, err := codex.Implement(context.Background(), &Record{RunID: "r1"}, t.TempDir(), "task", time.Minute)
	if err != nil || report.Outcome != ImplFailed {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestImplementReturnsCancelDuringMCPList(t *testing.T) {
	codex, _ := codexFor(t, blockingRunner{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := codex.Implement(ctx, &Record{RunID: "r1"}, t.TempDir(), "task", time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

// 次のラウンドでリンク先を上書きせず、実体の schema を用意する。
func TestImplementDoesNotFollowSchemaLinks(t *testing.T) {
	for _, link := range []string{"directory", "symlink", "hardlink"} {
		t.Run(link, func(t *testing.T) {
			work, outside := t.TempDir(), t.TempDir()
			target := filepath.Join(outside, "sentinel")
			if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			run := filepath.Join(work, ".agent-run")
			if link == "directory" {
				if err := os.Symlink(outside, run); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(run, 0700); err != nil {
					t.Fatal(err)
				}
				makeLink := os.Symlink
				if link == "hardlink" {
					makeLink = os.Link
				}
				if err := makeLink(target, filepath.Join(run, "result.schema.json")); err != nil {
					t.Fatal(err)
				}
			}
			runner := &scriptRunner{responses: map[string]response{codexMCP: {out: `[]`}, codexExec: {files: resultFiles(0)}}}
			codex, _ := codexFor(t, runner)
			for range 2 {
				if _, err := codex.Implement(context.Background(), &Record{RunID: "r1"}, work, "task", time.Minute); err != nil {
					t.Fatal(err)
				}
			}
			if body, err := os.ReadFile(target); err != nil || string(body) != "keep" {
				t.Fatalf("target=%q err=%v", body, err)
			}
			if _, err := os.Stat(filepath.Join(outside, "result.schema.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("write escaped: %v", err)
			}
			if body, err := os.ReadFile(filepath.Join(run, "result.schema.json")); err != nil || string(body) != ResultSchema {
				t.Fatalf("schema=%q err=%v", body, err)
			}
		})
	}
}

type sinkFailureRunner struct{ canceled bool }

func (r *sinkFailureRunner) Run(ctx context.Context, cmd Cmd) (string, error) {
	if cmd.Stdout == nil {
		return "[]", nil
	}
	_, err := cmd.Stdout.Write([]byte(threadOK))
	r.canceled = errors.Is(ctx.Err(), context.Canceled)
	return "", err
}
func TestImplementCancelsWhenThreadSaveFails(t *testing.T) {
	runner := &sinkFailureRunner{}
	codex, _ := codexFor(t, runner)
	saveErr := errors.New("cannot persist thread")
	codex.Save = func(*Record) error { return saveErr }
	_, err := codex.Implement(context.Background(), &Record{RunID: "r1"}, t.TempDir(), "task", time.Minute)
	if !errors.Is(err, saveErr) || !runner.canceled {
		t.Fatalf("err=%v canceled=%v", err, runner.canceled)
	}
}
