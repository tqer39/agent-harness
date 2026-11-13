//go:build !windows

package harness

import "testing"

func TestProfileMatchesIgnoresHarnessTrust(t *testing.T) {
	const root = "/Users/u/workspace/tqer39"
	profile := []byte("default_permissions = \"agent-harness\"\n\n[permissions.agent-harness]\nextends = \":workspace\"\n")
	trusted := string(profile) + "\n[projects.\"/Users/u/workspace/tqer39/tts-partner-worktrees/agent-20261108-053323-f956\"]\ntrust_level = \"trusted\"\n"
	if !ProfileMatches([]byte(trusted), profile, root) {
		t.Fatal("trust written by Codex for a harness clone was treated as a change")
	}
	twice := trusted + "\n[projects.\"/Users/u/workspace/tqer39/tts-partner-worktrees/agent-20261108-055733-cea5\"]\ntrust_level = \"trusted\"\n"
	if !ProfileMatches([]byte(twice), profile, root) {
		t.Fatal("trust for several runs was treated as a change")
	}
	for name, changed := range map[string]string{
		"他のプロジェクトの信頼": string(profile) + "\n[projects.\"/Users/u/workspace/tqer39/other\"]\ntrust_level = \"trusted\"\n",
		"信頼以外の設定":     string(profile) + "\n[projects.\"/Users/u/x-worktrees/agent-20261108-053323-f956\"]\ntrust_level = \"trusted\"\nsandbox_mode = \"danger-full-access\"\n",
		"権限の変更":       "default_permissions = \"agent-harness\"\n\n[permissions.agent-harness]\nextends = \":danger\"\n",
		"節の後の追記":      trusted + "web_search = \"live\"\n",
		"作業場所の外の同名":   string(profile) + "\n[projects.\"/tmp/x-worktrees/agent-20261108-053323-f956\"]\ntrust_level = \"trusted\"\n",
	} {
		if ProfileMatches([]byte(changed), profile, root) {
			t.Errorf("%s was ignored", name)
		}
	}
}
