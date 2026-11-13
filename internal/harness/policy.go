//go:build !windows

package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

type Change struct {
	Status  byte
	OldMode string
	Mode    string
	Path    string
}

type Finding struct {
	Path   string
	Reason string
}

type Policy struct{ StopPaths []string }

type RepoConfig struct {
	Policy        Policy
	Verify        []string
	RequiredCheck string
	// ExternalReviewCheck が空なら外部レビューを待たない。
	ExternalReviewCheck   string
	ExternalReviewAuthors []string
}

// DefaultPolicy は人間の確認で止めるパスを定義する。
func DefaultPolicy() Policy {
	return Policy{StopPaths: []string{
		".github/workflows/**", "**/AGENTS.md", "**/CLAUDE.md", ".codex/**", ".claude/**", ".rulesync/**",
		".agent-harness/**", ".standards/**", "lefthook.yml", "mise.toml",
		"**/*auth*/**", "**/*auth*.*", "**/*secret*", "**/*credential*", "**/*password*", "**/.env*", "**/*.pem", "**/*.key",
		"**/iam/**", "**/*iam*.*",
		"**/go.mod", "**/go.sum", "**/package.json", "**/package-lock.json", "**/pnpm-lock.yaml", "**/yarn.lock",
		"**/pyproject.toml", "**/uv.lock", "**/requirements*.txt", "**/Cargo.toml", "**/Cargo.lock",
		"**/*.tf", "**/*.tfvars", "**/migrations/**", "**/*migration*",
	}}
}

var testPath = regexp.MustCompile(`(^|/)(tests?|__tests__|spec)/|_test\.go$|\.(test|spec)\.[jt]sx?$|(^|/)test_[^/]*\.py$|_test\.py$`)

func globRegexp(pattern string) *regexp.Regexp {
	var b strings.Builder
	// ファイル名の改行で停止パスをすり抜けないよう、. を改行にも一致させる。
	b.WriteString("(?is)^")
	for i := 0; i < len(pattern); {
		switch {
		case strings.HasPrefix(pattern[i:], "**/"):
			b.WriteString("(.*/)?")
			i += 3
		case strings.HasPrefix(pattern[i:], "**"):
			b.WriteString(".*")
			i += 2
		case pattern[i] == '*':
			b.WriteString("[^/]*")
			i++
		default:
			_, size := utf8.DecodeRuneInString(pattern[i:])
			b.WriteString(regexp.QuoteMeta(pattern[i : i+size]))
			i += size
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// Evaluate は人間の確認が必要な変更を返す。LLM の判断に依存しない。
func Evaluate(changes []Change, policy Policy, pinned []string) []Finding {
	matchers := make([]*regexp.Regexp, 0, len(policy.StopPaths))
	for _, pattern := range policy.StopPaths {
		matchers = append(matchers, globRegexp(pattern))
	}
	var findings []Finding
	for _, change := range changes {
		reason := ""
		switch {
		case slices.Contains(pinned, change.Path):
			reason = "固定した受入テストの変更"
		case isLinkMode(change.OldMode) || isLinkMode(change.Mode):
			reason = "シンボリックリンクまたはサブモジュールの変更"
		case change.Status == 'D' && testPath.MatchString(change.Path):
			reason = "テストの削除"
		default:
			for i, matcher := range matchers {
				if matcher.MatchString(change.Path) {
					reason = "人間の確認が必要なパス: " + policy.StopPaths[i]
					break
				}
			}
		}
		if reason != "" {
			findings = append(findings, Finding{Path: change.Path, Reason: reason})
		}
	}
	return findings
}

func isLinkMode(mode string) bool { return mode == "120000" || mode == "160000" }

// ParseRaw は git diff --raw --no-renames -z の出力を解釈する。
func ParseRaw(raw string) ([]Change, error) {
	parts := strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00")
	if raw == "" {
		return nil, nil
	}
	if len(parts)%2 != 0 {
		return nil, errors.New("git diff --raw の出力が不正です")
	}
	changes := make([]Change, 0, len(parts)/2)
	for i := 0; i < len(parts); i += 2 {
		fields := strings.Fields(parts[i])
		if len(fields) != 5 || !strings.HasPrefix(fields[0], ":") || fields[4] == "" {
			return nil, fmt.Errorf("git diff --raw の行が不正です: %q", parts[i])
		}
		oldMode := strings.TrimPrefix(fields[0], ":")
		mode := fields[1]
		if fields[4][0] == 'D' {
			mode = oldMode
		}
		changes = append(changes, Change{Status: fields[4][0], OldMode: oldMode, Mode: mode, Path: parts[i+1]})
	}
	return changes, nil
}

// LoadRepoConfig は基準ブランチの checkout から設定を読む。既定の停止パスは削除できない。
func LoadRepoConfig(baseDir string) (RepoConfig, error) {
	config := RepoConfig{
		Policy:                DefaultPolicy(),
		Verify:                []string{"mise", "run", "check"},
		RequiredCheck:         "workflow-result",
		ExternalReviewAuthors: []string{"coderabbitai"},
	}
	data, err := os.ReadFile(filepath.Join(baseDir, ".agent-harness", "policy.json"))
	if errors.Is(err, os.ErrNotExist) {
		return config, nil
	}
	if err != nil {
		return config, err
	}
	var file struct {
		StopPaths     []string `json:"stop_paths"`
		Verify        []string `json:"verify"`
		RequiredCheck string   `json:"required_check"`
		// CodeRabbit はシートのない App の PR をレビューしないので、指定したときだけ待つ。
		ExternalReviewCheck   string   `json:"external_review_check"`
		ExternalReviewAuthors []string `json:"external_review_authors"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return config, fmt.Errorf(".agent-harness/policy.json: %w", err)
	}
	config.Policy.StopPaths = append(config.Policy.StopPaths, file.StopPaths...)
	if len(file.Verify) > 0 {
		config.Verify = file.Verify
	}
	if file.RequiredCheck != "" {
		config.RequiredCheck = file.RequiredCheck
	}
	config.ExternalReviewCheck = file.ExternalReviewCheck
	if len(file.ExternalReviewAuthors) > 0 {
		config.ExternalReviewAuthors = file.ExternalReviewAuthors
	}
	return config, nil
}
