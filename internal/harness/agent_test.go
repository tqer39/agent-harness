//go:build !windows

package harness

import (
	"slices"
	"strings"
	"testing"
)

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

func TestQuotaPatternDetectsAPIBudgetExhaustion(t *testing.T) {
	for _, text := range []string{"insufficient_quota", "You exceeded your current quota, please check your plan"} {
		if !quotaPattern.MatchString(text) {
			t.Errorf("%q was not treated as quota exhaustion", text)
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
