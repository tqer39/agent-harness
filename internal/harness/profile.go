//go:build !windows

package harness

import (
	"bytes"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// harnessProject はハーネスが作った作業用 clone の projects 節である。
	projectHeader = regexp.MustCompile(`^\[projects\.(?:"([^"]*)"|'([^']*)')\]$`)
	harnessClone  = regexp.MustCompile(`-worktrees/agent-\d{8}-\d{6}-[0-9a-f]{4}$`)
	trustedLine   = regexp.MustCompile(`^trust_level\s*=\s*"trusted"$`)
)

// ProfileMatches は権限プロファイルが埋め込みの版と同じかを返す。
// 信頼の確認に答えると、Codex は作業用 clone の信頼設定をプロファイルに書き足す。
// 権限は変わらないので、workRoot の下にハーネスが作る clone の節だけを無視して比べる。
// 他の追記や変更は差分として扱う。
func ProfileMatches(current, profile []byte, workRoot string) bool {
	return bytes.Equal(withoutHarnessProjects(current, workRoot), withoutHarnessProjects(profile, workRoot))
}

// isHarnessProject は節の見出しが workRoot/<name>-worktrees/agent-<run_id> の projects 節かを返す。
func isHarnessProject(line, workRoot string) bool {
	match := projectHeader.FindStringSubmatch(line)
	if match == nil {
		return false
	}
	path := match[1] + match[2]
	return harnessClone.MatchString(path) && filepath.Dir(filepath.Dir(path)) == filepath.Clean(workRoot)
}

func withoutHarnessProjects(data []byte, workRoot string) []byte {
	lines := strings.Split(string(data), "\n")
	kept := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		if !isHarnessProject(strings.TrimSpace(lines[i]), workRoot) {
			kept = append(kept, lines[i])
			continue
		}
		end := i + 1
		for end < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[end]), "[") {
			end++
		}
		body := lines[i+1 : end]
		if !onlyTrust(body) {
			// 信頼以外の設定を含む節は、権限を変えうるので比較に残す。
			kept = append(kept, lines[i])
			continue
		}
		i = end - 1
	}
	// 節を取り除いた後に残る空行の違いは無視する。
	return []byte(strings.TrimRight(strings.Join(kept, "\n"), "\n"))
}

func onlyTrust(body []string) bool {
	for _, line := range body {
		line = strings.TrimSpace(line)
		if line != "" && !trustedLine.MatchString(line) {
			return false
		}
	}
	return true
}
