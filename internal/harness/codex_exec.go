//go:build !windows

package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// ImplReport は実装担当の 1 回の実行の結果である。
type ImplReport struct {
	Outcome ImplOutcome
	Result  Result
	// Detail は ImplFailed と ImplExited の理由である。停止の理由に載せる。
	Detail string
}

// CodexExec は実装担当を codex exec、レビュー担当を claude -p の子プロセスとして起動する。
// 子プロセスには固定した環境変数だけを渡すので、Orca の端末の ORCA_* は届かない。
type CodexExec struct {
	Run    Runner
	Codex  string
	Claude string
	// CodexHome が空でなければ、この Codex ホームで実装担当を動かす。
	CodexHome string
	// Events には実行ごとのイベントを <run_id>.events.jsonl として残す。
	Events string
	// Out には実装とレビューの経過を表示する。
	Out io.Writer
	// Save が nil でなければ、thread_id を受け取った時点で実行記録を保存する。
	Save func(*Record) error
}

func (c *CodexExec) env(work string) []string {
	// 検証と同じ書き込み先を渡す。値はパスと設定だけで秘密情報ではない。
	env := sandboxEnv(work)
	if c.CodexHome != "" {
		env = append(env, "CODEX_HOME="+c.CodexHome)
	}
	return env
}

// enabledMCP は有効な MCP サーバーを返す。MCP にはドメイン制限が効かないので、実装担当では止める。
func (c *CodexExec) enabledMCP(ctx context.Context) ([]string, error) {
	var env []string
	if c.CodexHome != "" {
		env = []string{"CODEX_HOME=" + c.CodexHome}
	}
	out, err := c.Run.Run(ctx, Cmd{Name: c.Codex, Args: []string{"-p", "agent-harness", "mcp", "list", "--json"}, Env: env})
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

func (c *CodexExec) implementArgs(rec *Record, work string, disabledMCP []string) []string {
	run := filepath.Join(work, ".agent-run")
	args := []string{"-p", "agent-harness", "exec"}
	if rec.ThreadID != "" {
		args = append(args, "resume")
	}
	args = append(args, "--json", "-m", rec.Model,
		"-c", fmt.Sprintf("model_reasoning_effort=%q", rec.Effort),
		"-c", fmt.Sprintf("projects.%q.trust_level=%q", work, "trusted"),
	)
	for _, name := range disabledMCP {
		args = append(args, "-c", "mcp_servers."+name+".enabled=false")
	}
	args = append(args, "--output-schema", filepath.Join(run, "result.schema.json"), "-o", filepath.Join(run, "result.json"))
	if rec.ThreadID != "" {
		args = append(args, rec.ThreadID)
	}
	// 修正の依頼には CI のログなどが入り長くなるので、引数ではなく標準入力で渡す。
	return append(args, "-")
}

// Implement は実装担当を 1 回実行し、終了まで待つ。rec.ThreadID があれば同じ thread で続ける。
func (c *CodexExec) Implement(ctx context.Context, rec *Record, work, prompt string, limit time.Duration) (ImplReport, error) {
	// MCP の一覧の取得も含めて、ラウンドの時間上限で打ち切る。
	runCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	disabled, err := c.enabledMCP(runCtx)
	if err != nil {
		if ctx.Err() != nil {
			return ImplReport{}, ctx.Err()
		}
		if runCtx.Err() != nil {
			return ImplReport{Outcome: ImplTimeout}, nil
		}
		return ImplReport{}, err
	}
	run := filepath.Join(work, ".agent-run")
	// 前のラウンドが残したリンクをホスト側の書き込みでたどらない。
	if info, err := os.Lstat(run); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(run); err != nil {
			return ImplReport{}, err
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return ImplReport{}, err
	}
	if err := os.MkdirAll(run, 0o700); err != nil {
		return ImplReport{}, err
	}
	schemaPath := filepath.Join(run, "result.schema.json")
	if err := os.Remove(schemaPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ImplReport{}, err
	}
	schema, err := os.OpenFile(schemaPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return ImplReport{}, err
	}
	_, writeErr := schema.Write([]byte(ResultSchema))
	if err := errors.Join(writeErr, schema.Close()); err != nil {
		return ImplReport{}, err
	}
	// ReadResult は run_id と round しか見ないので、前の実行が残した同じ round の結果を消す。
	if err := os.Remove(filepath.Join(run, "result.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ImplReport{}, err
	}
	if err := os.MkdirAll(c.Events, 0o700); err != nil {
		return ImplReport{}, err
	}
	log, err := os.OpenFile(filepath.Join(c.Events, rec.RunID+".events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return ImplReport{}, err
	}
	defer log.Close()
	// thread は受け取った時点で記録し、中断されても同じ thread で再開できるようにする。
	onThread := func(id string) error {
		if rec.ThreadID != "" {
			return nil
		}
		rec.ThreadID = id
		if c.Save == nil {
			return nil
		}
		return c.Save(rec)
	}
	events := &codexEvents{log: log, out: c.Out, work: work, onThread: onThread, stop: cancel}
	fmt.Fprintf(c.Out, "実装: round %d を開始します\n", rec.Round)
	_, runErr := c.Run.Run(runCtx, Cmd{Dir: work, Name: c.Codex, Args: c.implementArgs(rec, work, disabled), Env: c.env(work), Stdin: prompt, Stdout: events})
	if err := events.Flush(); err != nil {
		return ImplReport{}, err
	}
	if ctx.Err() != nil {
		return ImplReport{}, ctx.Err()
	}
	// 子孫の後始末で上限を越えても、結果が書かれていれば完了として扱う。
	// 再接続などの一時的なエラーの後に完了することもあるので、結果を先に確かめる。
	// 書き込み途中なら解析に失敗するので、結果を優先しても取り違えない。
	result, ok, readErr := ReadResult(work, rec.RunID, rec.Round)
	if readErr == nil && ok {
		return ImplReport{Outcome: ImplDone, Result: result}, nil
	}
	if runCtx.Err() != nil {
		return ImplReport{Outcome: ImplTimeout}, nil
	}
	var exitErr *RunError
	if runErr != nil && !errors.As(runErr, &exitErr) {
		return ImplReport{}, runErr
	}
	if readErr != nil {
		return ImplReport{}, readErr
	}
	stderr := ""
	if exitErr != nil {
		stderr = exitErr.Stderr
	}
	// 失敗の本文があれば本文だけで判定し、stderr の一時的な 429 などで取り違えない。
	quotaText := events.Failure
	if quotaText == "" {
		quotaText = stderr
	}
	if quotaPattern.MatchString(quotaText) {
		return ImplReport{Outcome: ImplQuota}, nil
	}
	if events.Failure != "" {
		return ImplReport{Outcome: ImplFailed, Detail: events.Failure}, nil
	}
	return ImplReport{Outcome: ImplExited, Detail: tail(stderr, 4000)}, nil
}

// Review は読み取り専用の Claude を子プロセスで実行し、判定を標準出力で受け取る。
func (c *CodexExec) Review(ctx context.Context, rec *Record, dir, prompt string, limit time.Duration) (ReviewOutcome, []byte, error) {
	fmt.Fprintf(c.Out, "レビュー: %s で開始します\n", rec.ReviewerModel)
	runCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	args := reviewArgs(c.Claude, rec.ReviewerModel, prompt)
	out, err := c.Run.Run(runCtx, Cmd{Dir: dir, Name: args[0], Args: args[1:]})
	if ctx.Err() != nil {
		return 0, nil, ctx.Err()
	}
	if runCtx.Err() != nil {
		fmt.Fprintln(c.Out, "レビュー: 時間上限に達しました")
		return ReviewTimeout, nil, nil
	}
	var runErr *RunError
	if errors.As(err, &runErr) {
		if quotaPattern.MatchString(runErr.Stderr) || quotaPattern.MatchString(runErr.Stdout) {
			fmt.Fprintln(c.Out, "レビュー: 利用上限に達しました")
			return ReviewQuota, nil, nil
		}
		fmt.Fprintf(c.Out, "レビュー: 失敗しました (exit %d)\n", runErr.Code)
		return ReviewFailed, []byte(runErr.Stdout), nil
	}
	if err != nil {
		return 0, nil, err
	}
	fmt.Fprintln(c.Out, "レビュー: 完了しました")
	return ReviewDone, []byte(out), nil
}
