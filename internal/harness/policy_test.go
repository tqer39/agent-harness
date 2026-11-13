//go:build !windows

package harness

import (
	"os"
	"path/filepath"
	"testing"
)

func reasons(findings []Finding) map[string]string {
	result := map[string]string{}
	for _, finding := range findings {
		result[finding.Path] = finding.Reason
	}
	return result
}

func TestEvaluateStopsSensitivePaths(t *testing.T) {
	for _, path := range []string{
		".github/workflows/ci.yml",
		"AGENTS.md",
		"sub/CLAUDE.md",
		".codex/hooks.json",
		"internal/auth/login.go",
		"config/secrets.yaml",
		".env.production",
		"go.mod",
		"package-lock.json",
		"uv.lock",
		"infra/main.tf",
		"db/migrations/001.sql",
		".agent-harness/policy.json",
		"lefthook.yml",
	} {
		got := Evaluate([]Change{{Status: 'M', Mode: "100644", Path: path}}, DefaultPolicy(), nil)
		if len(got) == 0 {
			t.Errorf("%s was not stopped", path)
		}
	}
}

func TestEvaluateAllowsOrdinaryCode(t *testing.T) {
	changes := []Change{
		{Status: 'M', Mode: "100644", Path: "src/author_name.txt.go"},
		{Status: 'A', Mode: "100644", Path: "internal/report/report.go"},
		{Status: 'M', Mode: "100644", Path: "internal/report/report_test.go"},
	}
	policy := Policy{StopPaths: []string{"**/secrets*"}}
	if got := Evaluate(changes, policy, nil); len(got) != 0 {
		t.Fatalf("got=%+v", got)
	}
}

func TestEvaluateStopsTestDeletionPinnedChangeAndLinks(t *testing.T) {
	got := reasons(Evaluate([]Change{
		{Status: 'D', Mode: "000000", Path: "pkg/a_test.go"},
		{Status: 'M', Mode: "100644", Path: "tests/acceptance.py"},
		{Status: 'A', Mode: "120000", Path: "link"},
		{Status: 'A', Mode: "160000", Path: "sub"},
	}, Policy{}, []string{"tests/acceptance.py"}))
	for _, path := range []string{"pkg/a_test.go", "tests/acceptance.py", "link", "sub"} {
		if got[path] == "" {
			t.Errorf("%s was not stopped: %+v", path, got)
		}
	}
}

func TestParseRaw(t *testing.T) {
	raw := ":100644 100644 aaa bbb M\x00日本語/a.go\x00:000000 120000 000 ccc A\x00link\x00"
	changes, err := ParseRaw(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 || changes[0].Path != "日本語/a.go" || changes[1].Mode != "120000" || changes[1].Status != 'A' {
		t.Fatalf("changes=%+v", changes)
	}
}

func TestParseRawRejectsMalformed(t *testing.T) {
	if _, err := ParseRaw(":100644 aaa\x00a\x00"); err == nil {
		t.Fatal("malformed input was accepted")
	}
}

func TestLoadRepoConfigAddsStopPathsToDefaults(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".agent-harness"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"stop_paths":["billing/**"],"verify":["make","test"],"required_check":"ci-ok"}`
	if err := os.WriteFile(filepath.Join(dir, ".agent-harness", "policy.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	config, err := LoadRepoConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if config.RequiredCheck != "ci-ok" || config.Verify[0] != "make" {
		t.Fatalf("config=%+v", config)
	}
	got := Evaluate([]Change{{Status: 'M', Mode: "100644", Path: "billing/x.go"}}, config.Policy, nil)
	if len(got) != 1 {
		t.Fatalf("got=%+v", got)
	}
	got = Evaluate([]Change{{Status: 'M', Mode: "100644", Path: "AGENTS.md"}}, config.Policy, nil)
	if len(got) != 1 {
		t.Fatalf("defaults were removed: %+v", got)
	}
}

func TestLoadRepoConfigDefaults(t *testing.T) {
	config, err := LoadRepoConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if config.RequiredCheck != "workflow-result" || len(config.Verify) == 0 {
		t.Fatalf("config=%+v", config)
	}
}

func TestEvaluateStopsPythonSuffixTestDeletion(t *testing.T) {
	got := Evaluate([]Change{{Status: 'D', Mode: "100644", Path: "src/report_test.py"}}, Policy{}, nil)
	if len(got) != 1 {
		t.Fatalf("got=%+v", got)
	}
}

func TestEvaluateStopsLinkReplacedByFile(t *testing.T) {
	got := Evaluate([]Change{{Status: 'T', OldMode: "120000", Mode: "100644", Path: "link"}}, Policy{}, nil)
	if len(got) != 1 {
		t.Fatalf("got=%+v", got)
	}
}

func TestParseRawKeepsOldMode(t *testing.T) {
	changes, err := ParseRaw(":120000 100644 aaa bbb T\x00link\x00")
	if err != nil || changes[0].OldMode != "120000" || changes[0].Mode != "100644" {
		t.Fatalf("changes=%+v err=%v", changes, err)
	}
}

func TestStopPathsMatchNewlines(t *testing.T) {
	got := Evaluate([]Change{{Status: 'M', Mode: "100644", Path: ".github/workflows/ci\nextra.yml"}}, DefaultPolicy(), nil)
	if len(got) != 1 {
		t.Fatalf("got=%+v", got)
	}
}
