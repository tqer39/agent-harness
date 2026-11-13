//go:build !windows

package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// codexEvents は codex exec --json の JSONL を受け取り、そのまま記録しながら 1 行ずつの表示に変える。
type codexEvents struct {
	log  io.Writer
	out  io.Writer
	work string
	// pending は改行で終わっていない受信途中の行である。
	pending []byte
	// onThread は thread.started を受け取った時点で呼ぶ。中断されても thread を残せるようにする。
	onThread func(string) error
	// err は最初の処理エラーである。exec はプロセスの終了エラーを優先して返すので、Flush で返す。
	err error
	// stop は記録や表示が失敗したとき、子プロセスを直ちに止める。
	stop func()

	ThreadID string
	// Failure は turn.failed と最上位の error の本文である。最後のものを残す。
	Failure string
}

type codexEvent struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Message  string `json:"message"`
	Error    *struct {
		Message string `json:"message"`
	} `json:"error"`
	Item *struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Message  string `json:"message"`
		Command  string `json:"command"`
		ExitCode *int   `json:"exit_code"`
		Changes  []struct {
			Path string `json:"path"`
			Kind string `json:"kind"`
		} `json:"changes"`
	} `json:"item"`
}

func (e *codexEvents) Write(p []byte) (int, error) {
	if e.err != nil {
		return len(p), e.err
	}
	e.pending = append(e.pending, p...)
	for {
		i := bytes.IndexByte(e.pending, '\n')
		if i < 0 {
			return len(p), nil
		}
		line := e.pending[:i]
		e.pending = e.pending[i+1:]
		if err := e.handle(line); err != nil {
			e.err = err
			if e.stop != nil {
				e.stop()
			}
			return len(p), err
		}
	}
}

// Flush はプロセスの終了後に、改行で終わらなかった最後の行を処理する。
func (e *codexEvents) Flush() error {
	if e.err != nil {
		return e.err
	}
	line := e.pending
	e.pending = nil
	return e.handle(line)
}

func (e *codexEvents) handle(line []byte) error {
	if len(bytes.TrimSpace(line)) == 0 {
		return nil
	}
	record := append(append([]byte{}, line...), '\n')
	if _, err := e.log.Write(record); err != nil {
		return err
	}
	var event codexEvent
	if err := json.Unmarshal(line, &event); err != nil {
		// JSON でない行も失わないよう、そのまま表示する。
		_, err := fmt.Fprintln(e.out, e.relative(string(line)))
		return err
	}
	if event.Type == "thread.started" && e.onThread != nil {
		if err := e.onThread(event.ThreadID); err != nil {
			return err
		}
	}
	text := e.describe(event)
	if text == "" {
		return nil
	}
	_, err := fmt.Fprintln(e.out, e.relative(text))
	return err
}

func (e *codexEvents) describe(event codexEvent) string {
	switch event.Type {
	case "thread.started":
		e.ThreadID = event.ThreadID
		return "thread " + event.ThreadID
	case "turn.failed":
		if event.Error != nil {
			e.Failure = event.Error.Message
		}
		return "失敗: " + e.Failure
	case "error":
		e.Failure = event.Message
		return "エラー: " + event.Message
	case "item.completed":
		return describeItem(event)
	}
	return ""
}

func describeItem(event codexEvent) string {
	item := event.Item
	if item == nil {
		return ""
	}
	switch item.Type {
	case "command_execution":
		code := "?"
		if item.ExitCode != nil {
			code = strconv.Itoa(*item.ExitCode)
		}
		return fmt.Sprintf("$ %s (exit %s)", item.Command, code)
	case "file_change":
		parts := make([]string, 0, len(item.Changes))
		for _, change := range item.Changes {
			parts = append(parts, change.Kind+" "+change.Path)
		}
		return "変更: " + strings.Join(parts, ", ")
	case "agent_message":
		return "codex: " + item.Text
	case "error":
		// 実行が続く警告である。終了の判定には使わない。
		return "警告: " + item.Message
	}
	return ""
}

// relative は表示を短くするため、作業用 clone のパスを相対パスにする。
func (e *codexEvents) relative(text string) string {
	if e.work == "" {
		return text
	}
	return strings.ReplaceAll(text, strings.TrimSuffix(e.work, "/")+"/", "")
}
