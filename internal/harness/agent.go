//go:build !windows

package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

type ImplOutcome int

const (
	ImplDone ImplOutcome = iota
	ImplQuota
	ImplBlocked
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

var (
	// Codex の画面には「You have 1 usage limit reset available」が常に出るので、
	// "usage limit" だけでは一致させず、上限に達したことを示す表現に限る。
	quotaPattern = regexp.MustCompile(`(?i)hit your [\w ]*limit|usage limit (reached|exceeded)|rate limit reached|out of (credits|usage)|insufficient_quota|exceeded your current quota`)
	trustPattern = regexp.MustCompile(`(?i)do you trust the contents`)
)

// Herdr は hc で実装担当とレビュー担当を起動する。状態表示は合否に使わない。
type Herdr struct {
	Run    Runner
	HC     string
	Codex  string
	Claude string
	Poll   time.Duration
	// CodexHome が空でなければ、この Codex ホームで実装担当を動かす。
	CodexHome string
}

func implName(rec *Record) string   { return "impl-" + rec.RunID }
func reviewName(rec *Record) string { return "review-" + rec.RunID }

// enabledMCP は有効な MCP サーバーを返す。MCP にはドメイン制限が効かないので、実装担当では止める。
func (h *Herdr) enabledMCP(ctx context.Context) ([]string, error) {
	var env []string
	if h.CodexHome != "" {
		env = []string{"CODEX_HOME=" + h.CodexHome}
	}
	out, err := h.Run.Run(ctx, Cmd{Name: h.Codex, Args: []string{"-p", "agent-harness", "mcp", "list", "--json"}, Env: env})
	if err != nil {
		return nil, err
	}
	var servers []struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.Unmarshal([]byte(out), &servers); err != nil {
		return nil, fmt.Errorf("MCP の一覧を解釈できません: %w", err)
	}
	var names []string
	for _, server := range servers {
		if server.Enabled {
			names = append(names, server.Name)
		}
	}
	return names, nil
}

func (h *Herdr) implementerArgs(rec *Record, work, prompt string, disabledMCP []string) []string {
	// hc の端末は呼び出し元の環境を引き継がない。実装担当のコマンドにも検証と同じ書き込み先を渡す。
	// 値はパスと設定だけで秘密情報ではない。
	args := []string{"run", "--bg", "--json", "--name", implName(rec), "--cwd", work, "--no-parent", "--", "/usr/bin/env"}
	args = append(args, sandboxEnv(work)...)
	if h.CodexHome != "" {
		args = append(args, "CODEX_HOME="+h.CodexHome)
	}
	args = append(args,
		h.Codex, "-p", "agent-harness", "-m", rec.Model,
		"-c", fmt.Sprintf("model_reasoning_effort=%q", rec.Effort),
		"-c", fmt.Sprintf("projects.%q.trust_level=%q", work, "trusted"),
	)
	for _, name := range disabledMCP {
		args = append(args, "-c", "mcp_servers."+name+".enabled=false")
	}
	return append(args, "--ask-for-approval", "never", prompt)
}

func (h *Herdr) StartImplementer(ctx context.Context, rec *Record, work, prompt string) error {
	disabled, err := h.enabledMCP(ctx)
	if err != nil {
		return err
	}
	_, err = h.Run.Run(ctx, Cmd{Name: h.HC, Args: h.implementerArgs(rec, work, prompt, disabled)})
	return err
}

func (h *Herdr) SendImplementer(ctx context.Context, rec *Record, text string) error {
	_, err := h.Run.Run(ctx, Cmd{Name: h.HC, Args: []string{"send", implName(rec), text}})
	return err
}

// CloseImplementer は完了後に端末を閉じる。失敗しても結果には影響しない。
func (h *Herdr) CloseImplementer(ctx context.Context, rec *Record) {
	_, _ = h.Run.Run(ctx, Cmd{Name: h.HC, Args: []string{"kill", implName(rec)}})
	_, _ = h.Run.Run(ctx, Cmd{Name: h.HC, Args: []string{"rm", implName(rec)}})
}

// ImplementerAlive は実装担当の端末が生きているかを返す。
func (h *Herdr) ImplementerAlive(ctx context.Context, rec *Record) (bool, error) {
	out, err := h.Run.Run(ctx, Cmd{Name: h.HC, Args: []string{"get", implName(rec), "--json"}})
	var runErr *RunError
	if errors.As(err, &runErr) && runErr.Code == 3 {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var state struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(out), &state); err != nil {
		return false, err
	}
	return state.State == "live", nil
}

type waitState struct {
	Ended    bool `json:"ended"`
	ExitCode *int `json:"exit_code"`
}

func (h *Herdr) wait(ctx context.Context, name string) (waitState, error) {
	out, err := h.Run.Run(ctx, Cmd{Name: h.HC, Args: []string{"wait", name, "--timeout", "60", "--json"}})
	var runErr *RunError
	// blocked と finished は失敗ではない。画面と結果ファイルで判断する。
	if errors.As(err, &runErr) && (runErr.Code == 9 || runErr.Code == 11) {
		err = nil
		out = runErr.Stdout
	}
	if err != nil {
		return waitState{}, err
	}
	var state waitState
	if out != "" {
		if err := json.Unmarshal([]byte(out), &state); err != nil {
			return waitState{}, fmt.Errorf("hc wait の出力を解釈できません: %w", err)
		}
	}
	return state, nil
}

// WaitImplementer は結果ファイル、端末の終了、画面の順に確認する。
func (h *Herdr) WaitImplementer(ctx context.Context, rec *Record, work string, limit time.Duration) (ImplOutcome, Result, error) {
	deadline := time.Now().Add(limit)
	trustAnswered := false
	previous := ""
	for {
		result, ok, err := ReadResult(work, rec.RunID, rec.Round)
		if err != nil {
			return 0, Result{}, err
		}
		if ok {
			return ImplDone, result, nil
		}
		state, err := h.wait(ctx, implName(rec))
		if err != nil {
			return 0, Result{}, err
		}
		if state.Ended {
			// 待機中に結果を書いて終了した場合は、完了として扱う。
			result, ok, err = ReadResult(work, rec.RunID, rec.Round)
			if err != nil {
				return 0, Result{}, err
			}
			if ok {
				return ImplDone, result, nil
			}
			return ImplExited, Result{}, nil
		}
		screen, err := h.Run.Run(ctx, Cmd{Name: h.HC, Args: []string{"read", implName(rec), "--lines", "40"}})
		if err != nil {
			return 0, Result{}, err
		}
		// 作業中の出力にも同じ語が出るので、画面が止まっているときだけ利用上限とみなす。
		if quotaPattern.MatchString(screen) && screen == previous {
			return ImplQuota, Result{}, nil
		}
		previous = screen
		if trustPattern.MatchString(screen) {
			// -c の信頼設定は対話セッションで効かない。ハーネスが基準ブランチから作った
			// clone で、フックは無効、.codex への書き込みも拒否されるので 1 度だけ承認する。
			if trustAnswered {
				return ImplBlocked, Result{}, nil
			}
			if _, err := h.Run.Run(ctx, Cmd{Name: h.HC, Args: []string{"keys", implName(rec), "enter"}}); err != nil {
				return 0, Result{}, err
			}
			trustAnswered = true
		}
		if !time.Now().Before(deadline) {
			return ImplTimeout, Result{}, nil
		}
		select {
		case <-ctx.Done():
			return 0, Result{}, ctx.Err()
		case <-time.After(h.Poll):
		}
	}
}

func reviewArgs(claude, model, prompt string) []string {
	settings := `{"permissions":{"deny":["Read(~/.ssh/**)","Read(~/.aws/**)","Read(~/.config/gh/**)","Read(~/.config/op/**)","Read(~/.codex/**)","Read(~/.local/state/agent-harness/codex-api-home/**)"]}}`
	return []string{
		claude, "-p", "--model", model,
		"--output-format", "json", "--json-schema", VerdictSchema,
		"--tools", "Read,Grep,Glob", "--strict-mcp-config", "--settings", settings,
		prompt,
	}
}

// Review は読み取り専用の Claude を Herdr の端末で実行し、判定をファイルで受け取る。
func (h *Herdr) Review(ctx context.Context, rec *Record, dir, prompt string, limit time.Duration) (ReviewOutcome, []byte, error) {
	output := filepath.Join(dir, ".agent-run", "verdict.json")
	errPath := filepath.Join(dir, ".agent-run", "verdict.err")
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		return 0, nil, err
	}
	// 同じ名前の古い端末が残っていれば再利用できないので消す。結果の有無は問わない。
	_, _ = h.Run.Run(ctx, Cmd{Name: h.HC, Args: []string{"rm", reviewName(rec)}})
	args := append([]string{
		"run", "--bg", "--json", "--name", reviewName(rec), "--cwd", dir, "--no-parent", "--",
		"/bin/sh", "-c", `out="$1"; err="$2"; shift 2; exec "$@" >"$out" 2>"$err"`, "sh", output, errPath,
	}, reviewArgs(h.Claude, rec.ReviewerModel, prompt)...)
	if _, err := h.Run.Run(ctx, Cmd{Name: h.HC, Args: args}); err != nil {
		return 0, nil, err
	}
	deadline := time.Now().Add(limit)
	for {
		state, err := h.wait(ctx, reviewName(rec))
		if err != nil {
			return 0, nil, err
		}
		if state.Ended {
			if state.ExitCode != nil && *state.ExitCode == 0 {
				data, err := os.ReadFile(output)
				return ReviewDone, data, err
			}
			stderr, _ := os.ReadFile(errPath)
			data, _ := os.ReadFile(output)
			if quotaPattern.Match(stderr) || quotaPattern.Match(data) {
				return ReviewQuota, nil, nil
			}
			return ReviewFailed, data, nil
		}
		if !time.Now().Before(deadline) {
			return ReviewTimeout, nil, nil
		}
		select {
		case <-ctx.Done():
			return 0, nil, ctx.Err()
		case <-time.After(h.Poll):
		}
	}
}
