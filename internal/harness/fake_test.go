//go:build !windows

package harness

import (
	"context"
	"strings"
)

type response struct {
	out string
	err error
}

// scriptRunner はコマンド名、または「コマンド名 第 1 引数」で応答を決める。
type scriptRunner struct {
	responses map[string]response
	calls     []Cmd
}

func (r *scriptRunner) Run(_ context.Context, c Cmd) (string, error) {
	r.calls = append(r.calls, c)
	if len(c.Args) > 0 {
		if res, ok := r.responses[c.Name+" "+c.Args[0]]; ok {
			return res.out, res.err
		}
	}
	res := r.responses[c.Name]
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
