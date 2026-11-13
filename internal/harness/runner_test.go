//go:build !windows

package harness

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestOSRunnerDropsInheritedSecrets(t *testing.T) {
	t.Setenv("GH_TOKEN", "inherited")
	out, err := OSRunner{}.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "echo ${GH_TOKEN:-none}"}})
	if err != nil || strings.TrimSpace(out) != "none" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestOSRunnerPassesExplicitEnv(t *testing.T) {
	out, err := OSRunner{}.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "echo $FOO"}, Env: []string{"FOO=bar"}})
	if err != nil || strings.TrimSpace(out) != "bar" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestOSRunnerRedactsSecretsInErrors(t *testing.T) {
	_, err := OSRunner{}.Run(context.Background(), Cmd{
		Name:    "sh",
		Args:    []string{"-c", "echo leaked-value >&2; exit 3"},
		Secrets: []string{"leaked-value"},
	})
	var runErr *RunError
	if !errors.As(err, &runErr) || runErr.Code != 3 {
		t.Fatalf("err=%v", err)
	}
	if strings.Contains(err.Error(), "leaked-value") {
		t.Fatalf("secret was not redacted: %v", err)
	}
}

func TestBaseEnvKeepsPath(t *testing.T) {
	if os.Getenv("PATH") == "" {
		t.Skip("PATH is empty")
	}
	out, err := OSRunner{}.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "echo $PATH"}})
	if err != nil || strings.TrimSpace(out) == "" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestOSRunnerCombinesStderrOnRequest(t *testing.T) {
	out, err := OSRunner{}.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "echo err >&2"}, CombineOutput: true})
	if err != nil || strings.TrimSpace(out) != "err" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	out, err = OSRunner{}.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "echo err >&2"}})
	if err != nil || out != "" {
		t.Fatalf("stderr must stay separate by default: out=%q err=%v", out, err)
	}
}

func TestOSRunnerStreamsStdout(t *testing.T) {
	var got bytes.Buffer
	out, err := OSRunner{}.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "echo a; echo b"}, Stdout: &got})
	if err != nil || out != "" || got.String() != "a\nb\n" {
		t.Fatalf("out=%q got=%q err=%v", out, got.String(), err)
	}
}

// Orca の端末は起動トークンを含む ORCA_* を設定する。実装担当に渡さない。
func TestOSRunnerDropsOrcaVariables(t *testing.T) {
	t.Setenv("ORCA_LAUNCH_TOKEN", "secret")
	out, err := OSRunner{}.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "env | grep -c '^ORCA_' || true"}})
	if err != nil || strings.TrimSpace(out) != "0" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestOSRunnerStopsDescendantsOnCancel(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, _ = OSRunner{}.Run(ctx, Cmd{Name: "sh", Args: []string{"-c", "sleep 30 >/dev/null 2>&1 & echo $! > " + pidFile + "; wait"}})
	assertStopped(t, pidFile)
}

func TestOSRunnerStopsDescendantsLeftAfterExit(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	_, err := OSRunner{}.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "sleep 30 >/dev/null 2>&1 & echo $! > " + pidFile}})
	if err != nil {
		t.Fatal(err)
	}
	assertStopped(t, pidFile)
}

func assertStopped(t *testing.T, pidFile string) {
	t.Helper()
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	// シグナルの配送と回収は非同期なので、少し待つ。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("process %d is still running", pid)
}
