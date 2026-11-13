//go:build !windows

package harness

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type response struct {
	out string
	err error
	// stream は Cmd.Stdout が指定されたときに書く標準出力である。
	stream string
	// files は実行中に子プロセスが書くファイルである。キーは Cmd.Dir からの相対パス。
	files map[string]string
}

// scriptRunner は「コマンド名 引数...」の最も長く一致する前方部分、なければコマンド名で応答を決める。
type scriptRunner struct {
	responses map[string]response
	calls     []Cmd
}

func (r *scriptRunner) Run(_ context.Context, c Cmd) (string, error) {
	r.calls = append(r.calls, c)
	res := r.responses[c.Name]
	for n := len(c.Args); n > 0; n-- {
		if match, ok := r.responses[c.Name+" "+strings.Join(c.Args[:n], " ")]; ok {
			res = match
			break
		}
	}
	for name, body := range res.files {
		path := filepath.Join(c.Dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			return "", err
		}
	}
	if c.Stdout != nil && res.stream != "" {
		if _, err := io.WriteString(c.Stdout, res.stream); err != nil {
			return "", err
		}
	}
	return res.out, res.err
}

func (r *scriptRunner) called(name string, parts ...string) bool {
	for _, call := range r.calls {
		joined := strings.Join(call.Args, " ")
		ok := call.Name == name
		for _, part := range parts {
			ok = ok && strings.Contains(joined, part)
		}
		if ok {
			return true
		}
	}
	return false
}

// blockingRunner はキャンセルまで戻らない。listMCP なら MCP の一覧にだけすぐ答える。
type blockingRunner struct{ listMCP bool }

func (r blockingRunner) Run(ctx context.Context, c Cmd) (string, error) {
	if r.listMCP && strings.Contains(strings.Join(c.Args, " "), "mcp list") {
		return "[]", nil
	}
	<-ctx.Done()
	return "", &RunError{Name: c.Name, Code: -1}
}
