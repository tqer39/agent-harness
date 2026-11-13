//go:build !windows

package harness

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
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
