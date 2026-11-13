//go:build !windows

package harness

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestCodexEventsDisplaysAndRecords(t *testing.T) {
	var log, out bytes.Buffer
	events := &codexEvents{log: &log, out: &out, work: "/work"}
	input := strings.Join([]string{
		`{"type":"thread.started","thread_id":"t-1"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"i0","type":"error","message":"Under-development features enabled"}}`,
		`{"type":"item.started","item":{"id":"i1","type":"command_execution","command":"go test ./...","exit_code":null,"status":"in_progress"}}`,
		`{"type":"item.completed","item":{"id":"i1","type":"command_execution","command":"go test ./...","aggregated_output":"ok","exit_code":0,"status":"completed"}}`,
		`{"type":"item.completed","item":{"id":"i2","type":"file_change","changes":[{"path":"/work/a.go","kind":"update"}],"status":"completed"}}`,
		`{"type":"item.completed","item":{"id":"i3","type":"agent_message","text":"/work/a.go を直しました"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1}}`,
	}, "\n") + "\n"
	// 行の途中で区切って渡しても、1 行ずつ処理する。
	for _, chunk := range []string{input[:50], input[50:]} {
		if _, err := events.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if err := events.Flush(); err != nil {
		t.Fatal(err)
	}
	if log.String() != input {
		t.Fatalf("log=%q", log.String())
	}
	want := "thread t-1\n" +
		"警告: Under-development features enabled\n" +
		"$ go test ./... (exit 0)\n" +
		"変更: update a.go\n" +
		"codex: a.go を直しました\n"
	if out.String() != want {
		t.Fatalf("out=%q", out.String())
	}
	if events.ThreadID != "t-1" || events.Failure != "" {
		t.Fatalf("thread=%q failure=%q", events.ThreadID, events.Failure)
	}
}

func TestCodexEventsKeepsFailure(t *testing.T) {
	for _, line := range []string{
		`{"type":"turn.failed","error":{"message":"You've hit your usage limit."}}`,
		`{"type":"error","message":"You've hit your usage limit."}`,
	} {
		var log, out bytes.Buffer
		events := &codexEvents{log: &log, out: &out}
		// 最後の行は改行で終わらないことがあるので、Flush で処理する。
		if _, err := events.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
		if err := events.Flush(); err != nil {
			t.Fatal(err)
		}
		if events.Failure != "You've hit your usage limit." {
			t.Errorf("%s: failure=%q", line, events.Failure)
		}
	}
}

func TestCodexEventsReportsThreadWhenItStarts(t *testing.T) {
	var log, out bytes.Buffer
	var got []string
	events := &codexEvents{log: &log, out: &out, onThread: func(id string) error {
		got = append(got, id)
		return nil
	}}
	// 改行で終わった時点で通知し、プロセスの終了を待たない。
	if _, err := events.Write([]byte(`{"type":"thread.started","thread_id":"t-1"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "t-1" {
		t.Fatalf("got=%v", got)
	}
}

func TestCodexEventsShowsNonJSONLines(t *testing.T) {
	var log, out bytes.Buffer
	events := &codexEvents{log: &log, out: &out, work: "/work"}
	if _, err := events.Write([]byte("panic at /work/x.go\n")); err != nil {
		t.Fatal(err)
	}
	if out.String() != "panic at x.go\n" || log.String() != "panic at /work/x.go\n" {
		t.Fatalf("out=%q log=%q", out.String(), log.String())
	}
}

func TestCodexEventsKeepsFirstErrorForFlush(t *testing.T) {
	var log, out bytes.Buffer
	saveErr := errors.New("save failed")
	events := &codexEvents{log: &log, out: &out, onThread: func(string) error { return saveErr }}
	_, err := events.Write([]byte(`{"type":"thread.started","thread_id":"t-1"}` + "\n"))
	if !errors.Is(err, saveErr) {
		t.Fatalf("write err=%v", err)
	}
	// exec はプロセスの終了エラーを優先して返すので、Flush でも同じエラーを返す。
	if err := events.Flush(); !errors.Is(err, saveErr) {
		t.Fatalf("flush err=%v", err)
	}
}

type failingEventWriter struct{ err error }

func (w failingEventWriter) Write([]byte) (int, error) { return 0, w.err }
func TestCodexEventsStopsOnFirstSinkFailure(t *testing.T) {
	for _, sink := range []string{"log", "output", "thread"} {
		t.Run(sink, func(t *testing.T) {
			failure := errors.New("sink failed")
			stops := 0
			events := &codexEvents{log: io.Discard, out: io.Discard, stop: func() { stops++ }}
			switch sink {
			case "log":
				events.log = failingEventWriter{failure}
			case "output":
				events.out = failingEventWriter{failure}
			case "thread":
				events.onThread = func(string) error { return failure }
			}
			for range 2 {
				if _, err := events.Write([]byte(`{"type":"thread.started","thread_id":"t-1"}` + "\n")); !errors.Is(err, failure) {
					t.Fatalf("err=%v", err)
				}
			}
			if err := events.Flush(); !errors.Is(err, failure) || stops != 1 {
				t.Fatalf("err=%v stops=%d", err, stops)
			}
		})
	}
}
