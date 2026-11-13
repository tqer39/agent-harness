//go:build !windows

package harness

import (
	"context"
	"strings"
	"testing"
	"time"
)

type fixedToken string

func (f fixedToken) Token(context.Context) (string, error) { return string(f), nil }

func forgeWith(responses map[string]response) (*GitHub, *scriptRunner) {
	runner := &scriptRunner{responses: responses}
	return &GitHub{Run: runner, Tokens: fixedToken("t0k3n"), Repo: "o/n", Poll: time.Millisecond}, runner
}

func TestCreatePRParsesNumberAndPassesTokenOnlyByEnv(t *testing.T) {
	forge, runner := forgeWith(map[string]response{"gh pr": {out: "https://github.com/o/n/pull/17\n"}})
	number, err := forge.CreatePR(context.Background(), "agent/r1", "main", "title", "body")
	if err != nil || number != 17 {
		t.Fatalf("number=%d err=%v", number, err)
	}
	call := runner.calls[0]
	if call.Env[0] != "GH_TOKEN=t0k3n" || call.Secrets[0] != "t0k3n" || strings.Contains(strings.Join(call.Args, " "), "t0k3n") {
		t.Fatalf("call=%+v", call)
	}
}

func TestArmMergeFixesHeadSHA(t *testing.T) {
	forge, runner := forgeWith(map[string]response{})
	if err := forge.ArmMerge(context.Background(), 17, "abc"); err != nil {
		t.Fatal(err)
	}
	if !runner.called("gh", "pr merge 17", "--auto", "--squash", "--match-head-commit abc") {
		t.Fatalf("calls=%+v", runner.calls)
	}
}

func TestWaitCheckReadsConclusion(t *testing.T) {
	cases := map[string]bool{"success": true, "failure": false}
	for conclusion, want := range cases {
		body := `{"check_runs":[{"status":"completed","conclusion":"` + conclusion + `"}]}`
		forge, _ := forgeWith(map[string]response{"gh api": {out: body}})
		got, err := forge.WaitCheck(context.Background(), "abc", "workflow-result")
		if err != nil || got != want {
			t.Errorf("%s: got=%v err=%v", conclusion, got, err)
		}
	}
}

func TestWaitCheckRequiresEveryRun(t *testing.T) {
	// push と pull_request の両方で起動すると、同じ名前の check が 2 件できる。
	forge, _ := forgeWith(map[string]response{"gh api": {out: `{"check_runs":[{"status":"completed","conclusion":"success"},{"status":"completed","conclusion":"failure"}]}`}})
	if ok, err := forge.WaitCheck(context.Background(), "abc", "ci"); err != nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	forge, _ = forgeWith(map[string]response{"gh api": {out: `{"check_runs":[{"status":"completed","conclusion":"success"},{"status":"in_progress"}]}`}})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := forge.WaitCheck(ctx, "abc", "ci"); err == nil {
		t.Fatal("a running check was ignored")
	}
}

func TestWaitCheckEscapesName(t *testing.T) {
	forge, runner := forgeWith(map[string]response{"gh api": {out: `{"check_runs":[{"status":"completed","conclusion":"success"}]}`}})
	if _, err := forge.WaitCheck(context.Background(), "abc", "test (ubuntu-latest) & more"); err != nil {
		t.Fatal(err)
	}
	if !runner.called("gh", "check_name=test+%28ubuntu-latest%29+%26+more&filter=latest") {
		t.Fatalf("calls=%+v", runner.calls)
	}
}

func TestWaitCheckStopsAtContextDeadline(t *testing.T) {
	forge, _ := forgeWith(map[string]response{"gh api": {out: `{"check_runs":[]}`}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if _, err := forge.WaitCheck(ctx, "abc", "workflow-result"); err == nil {
		t.Fatal("timeout was ignored")
	}
}

func TestWaitMergedRejectsHeadChange(t *testing.T) {
	forge, _ := forgeWith(map[string]response{"gh pr": {out: `{"state":"OPEN","headRefOid":"other","mergeCommit":null}`}})
	if _, err := forge.WaitMerged(context.Background(), 17, "abc"); err == nil {
		t.Fatal("head change was ignored")
	}
}

func TestWaitMergedReturnsMergeSHA(t *testing.T) {
	forge, _ := forgeWith(map[string]response{"gh pr": {out: `{"state":"MERGED","headRefOid":"abc","mergeCommit":{"oid":"def"}}`}})
	sha, err := forge.WaitMerged(context.Background(), 17, "abc")
	if err != nil || sha != "def" {
		t.Fatalf("sha=%q err=%v", sha, err)
	}
}

func TestMarkNeedsHuman(t *testing.T) {
	forge, runner := forgeWith(map[string]response{})
	if err := forge.MarkNeedsHuman(context.Background(), 17, "理由"); err != nil {
		t.Fatal(err)
	}
	if !runner.called("gh", "label create needs-human") || !runner.called("gh", "pr edit 17", "--add-label needs-human") || !runner.called("gh", "pr comment 17", "理由") {
		t.Fatalf("calls=%+v", runner.calls)
	}
}

func TestOpenPullRequestFindsByHead(t *testing.T) {
	forge, runner := forgeWith(map[string]response{"gh pr": {out: `[{"number":55}]`}})
	number, err := forge.OpenPullRequest(context.Background(), "agent/revert-r1")
	if err != nil || number != 55 {
		t.Fatalf("number=%d err=%v", number, err)
	}
	if !runner.called("gh", "pr list", "--head agent/revert-r1", "--state open") {
		t.Fatalf("calls=%+v", runner.calls)
	}
	forge, _ = forgeWith(map[string]response{"gh pr": {out: `[]`}})
	if number, err := forge.OpenPullRequest(context.Background(), "x"); err != nil || number != 0 {
		t.Fatalf("number=%d err=%v", number, err)
	}
}

func TestPullRequestMergeReadsStateWithoutHeadCheck(t *testing.T) {
	forge, _ := forgeWith(map[string]response{"gh pr": {out: `{"state":"MERGED","mergeCommit":{"oid":"def"}}`}})
	state, err := forge.PullRequestMerge(context.Background(), 17)
	if err != nil || state.State != "MERGED" || state.MergeSHA != "def" || state.AutoMerge || state.NeedsHuman {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	forge, _ = forgeWith(map[string]response{"gh pr": {out: `{"state":"OPEN","mergeCommit":null,"autoMergeRequest":{"mergeMethod":"SQUASH"},"labels":[{"name":"needs-human"}]}`}})
	state, err = forge.PullRequestMerge(context.Background(), 17)
	if err != nil || !state.AutoMerge || !state.NeedsHuman {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}
