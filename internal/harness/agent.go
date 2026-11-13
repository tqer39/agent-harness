//go:build !windows

package harness

import "regexp"

type ImplOutcome int

const (
	ImplDone ImplOutcome = iota
	ImplQuota
	// ImplFailed は turn.failed や error のイベントで止まったことを表す。本文は ImplReport.Detail にある。
	ImplFailed
	ImplExited
	ImplTimeout
)

type ReviewOutcome int

const (
	ReviewDone ReviewOutcome = iota
	ReviewQuota
	ReviewFailed
	ReviewTimeout
)

// Codex の出力には「You have 1 usage limit reset available」が常に出るので、
// "usage limit" だけでは一致させず、上限に達したことを示す表現に限る。
var quotaPattern = regexp.MustCompile(`(?i)hit your [\w ]*limit|usage limit (reached|exceeded)|rate limit reached|out of (credits|usage)|insufficient_quota|exceeded your current quota`)

func reviewArgs(claude, model, prompt string) []string {
	settings := `{"permissions":{"deny":["Read(~/.ssh/**)","Read(~/.aws/**)","Read(~/.config/gh/**)","Read(~/.config/op/**)","Read(~/.codex/**)","Read(~/.local/state/agent-harness/codex-api-home/**)"]}}`
	return []string{
		claude, "-p", "--model", model,
		"--output-format", "json", "--json-schema", VerdictSchema,
		"--tools", "Read,Grep,Glob", "--strict-mcp-config", "--settings", settings,
		prompt,
	}
}
