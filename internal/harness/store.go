//go:build !windows

package harness

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type Status string

const (
	StatusImplementing    Status = "implementing"
	StatusWaitingQuota    Status = "waiting_quota"
	StatusStopped         Status = "stopped"
	StatusMergeArmed      Status = "merge_armed"
	StatusMergedUnchecked Status = "merged_unchecked"
	StatusCompleted       Status = "completed"
	StatusReverted        Status = "reverted"
)

// Record は 1 タスクの実行記録である。agent-telemetry もこの JSON を読む。
type Record struct {
	RunID            string      `json:"run_id"`
	Repo             string      `json:"repo"`
	Base             string      `json:"base"`
	BaseSHA          string      `json:"base_sha"`
	AcceptanceSHA    string      `json:"acceptance_sha"`
	Title            string      `json:"title"`
	Kind             string      `json:"kind"`
	Criteria         string      `json:"criteria"`
	Task             string      `json:"task"`
	Agent            string      `json:"agent"`
	Model            string      `json:"model"`
	Effort           string      `json:"effort"`
	CodexAuth        string      `json:"codex_auth,omitempty"`
	ReviewerModel    string      `json:"reviewer_model"`
	Status           Status      `json:"status"`
	StopReason       string      `json:"stop_reason,omitempty"`
	Pinned           []string    `json:"pinned,omitempty"`
	FedThreads       []FedThread `json:"fed_threads,omitempty"`
	Repairs          int         `json:"repairs"`
	Round            int         `json:"round"`
	PR               int         `json:"pr,omitempty"`
	HeadSHA          string      `json:"head_sha,omitempty"`
	MergeSHA         string      `json:"merge_sha,omitempty"`
	RevertPR         int         `json:"revert_pr,omitempty"`
	RevertSHA        string      `json:"revert_sha,omitempty"`
	FirstRoundPassed bool        `json:"first_round_passed"`
	// ReviewPending はレビュー担当の利用枠待ちである。push と PR は済んでいるので、再開時はレビューからやり直す。
	ReviewPending bool `json:"review_pending,omitempty"`
	// ThreadID は実装担当の Codex の thread である。修正の依頼と再開で同じ thread に送る。
	ThreadID  string    `json:"thread_id,omitempty"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Store struct{ Root string }

func NewRunID(now time.Time) string {
	suffix := make([]byte, 2)
	if _, err := rand.Read(suffix); err != nil {
		panic(err)
	}
	return now.UTC().Format("20060102-150405") + "-" + hex.EncodeToString(suffix)
}

func (s Store) runPath(id string) string { return filepath.Join(s.Root, "runs", id+".json") }

// repoKey は大文字と小文字を区別しない。GitHub では同じリポジトリを指すためである。
func repoKey(repo string) string { return strings.ReplaceAll(strings.ToLower(repo), "/", "__") }

func (s Store) lockPath(repo string) string {
	return filepath.Join(s.Root, "locks", repoKey(repo)+".lock")
}

func (s Store) stopPath(repo string) string {
	return filepath.Join(s.Root, "stops", repoKey(repo)+".txt")
}

func writePrivate(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(temp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

func (s Store) Save(rec *Record) error {
	rec.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(s.runPath(rec.RunID), append(data, '\n'))
}

func (s Store) Load(id string) (*Record, error) {
	data, err := os.ReadFile(s.runPath(id))
	if err != nil {
		return nil, err
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// List は指定した状態の実行記録を返す。
func (s Store) List(status Status) ([]*Record, error) {
	paths, err := filepath.Glob(filepath.Join(s.Root, "runs", "*.json"))
	if err != nil {
		return nil, err
	}
	var records []*Record
	for _, path := range paths {
		rec, err := s.Load(strings.TrimSuffix(filepath.Base(path), ".json"))
		if err != nil {
			return nil, err
		}
		if rec.Status == status {
			records = append(records, rec)
		}
	}
	return records, nil
}

// Lock は 1 リポジトリ 1 タスクの排他を取る。
// flock はプロセスの終了時にカーネルが解放するので、古いロックを回収する競合が起きない。
func (s Store) Lock(repo string) (func(), error) {
	path := s.lockPath(repo)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%s は別の実行が処理中です", repo)
		}
		return nil, err
	}
	return func() { file.Close() }, nil
}

func (s Store) Stopped(repo string) (string, error) {
	data, err := os.ReadFile(s.stopPath(repo))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return strings.TrimSpace(string(data)), err
}

func (s Store) SetStop(repo, reason string) error {
	return writePrivate(s.stopPath(repo), []byte(reason+"\n"))
}

func (s Store) ClearStop(repo string) error {
	err := os.Remove(s.stopPath(repo))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
