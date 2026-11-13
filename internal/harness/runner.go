//go:build !windows

// Package harness は受入条件の承認後の実装、レビュー、マージを無人で進める。
package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Cmd は 1 回のコマンド実行を表す。Env は明示的に渡す値だけを追加する。
type Cmd struct {
	Dir     string
	Name    string
	Args    []string
	Env     []string
	Stdin   string
	Secrets []string
	// CombineOutput は標準エラーも出力に含める。状態を標準エラーに出すコマンドに使う。
	CombineOutput bool
	// Stdout が nil でなければ、標準出力を逐次ここに渡す。戻り値の出力は空になる。
	Stdout io.Writer
}

type Runner interface {
	Run(context.Context, Cmd) (string, error)
}

type RunError struct {
	Name   string
	Code   int
	Stdout string
	Stderr string
}

func (e *RunError) Error() string {
	return fmt.Sprintf("%s failed with exit code %d: %s", e.Name, e.Code, e.Stderr)
}

// 継承する環境変数を固定し、親プロセスの認証情報を子プロセスへ渡さない。
var inheritedEnv = []string{"PATH", "HOME", "USER", "LANG", "LC_ALL", "TMPDIR", "SHELL", "TERM"}

type OSRunner struct{}

func (OSRunner) Run(ctx context.Context, c Cmd) (string, error) {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	cmd.Stdin = strings.NewReader(c.Stdin)
	env := []string{"GIT_TERMINAL_PROMPT=0"}
	for _, key := range inheritedEnv {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	cmd.Env = append(env, c.Env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	if c.Stdout != nil {
		cmd.Stdout = c.Stdout
	}
	cmd.Stderr = &stderr
	// 子孫のプロセスもまとめて止められるよう、プロセスグループを分ける。
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	// 子孫が出力を開いたまま残ると Wait が戻らないので、待機を打ち切る。
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	if cmd.Process != nil {
		// 終了後に残った子孫が作業用 clone を書き換え続けないよう、グループごと止める。
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if err == nil {
		if c.CombineOutput {
			return stdout.String() + stderr.String(), nil
		}
		return stdout.String(), nil
	}
	code := -1
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	}
	return stdout.String(), &RunError{
		Name:   c.Name,
		Code:   code,
		Stdout: redact(stdout.String(), c.Secrets),
		Stderr: redact(stderr.String(), c.Secrets),
	}
}

func redact(text string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[REDACTED]")
		}
	}
	return text
}
