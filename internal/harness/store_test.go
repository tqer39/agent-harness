//go:build !windows

package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreRoundTripUsesPrivateFile(t *testing.T) {
	store := Store{Root: t.TempDir()}
	rec := &Record{RunID: "r1", Repo: "o/n", Status: StatusImplementing, Effort: "medium"}
	if err := store.Save(rec); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load("r1")
	if err != nil || got.Repo != "o/n" || got.Effort != "medium" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	info, err := os.Stat(filepath.Join(store.Root, "runs", "r1.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v err=%v", info.Mode(), err)
	}
}

func TestLockIsExclusivePerRepo(t *testing.T) {
	store := Store{Root: t.TempDir()}
	unlock, err := store.Lock("o/n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Lock("o/n"); err == nil {
		t.Fatal("second lock succeeded")
	}
	if _, err := store.Lock("o/other"); err != nil {
		t.Fatalf("other repo: %v", err)
	}
	unlock()
	if _, err := store.Lock("o/n"); err != nil {
		t.Fatalf("after unlock: %v", err)
	}
}

func TestLockRecoversFromDeadProcess(t *testing.T) {
	store := Store{Root: t.TempDir()}
	path := store.lockPath("o/n")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	// プロセス ID の上限を超える値なので、存在しないプロセスとして扱われる。
	if err := os.WriteFile(path, []byte("99999999"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Lock("o/n"); err != nil {
		t.Fatalf("stale lock was not recovered: %v", err)
	}
}

func TestStopFlag(t *testing.T) {
	store := Store{Root: t.TempDir()}
	if reason, _ := store.Stopped("o/n"); reason != "" {
		t.Fatalf("reason=%q", reason)
	}
	if err := store.SetStop("o/n", "main の CI が失敗"); err != nil {
		t.Fatal(err)
	}
	if reason, _ := store.Stopped("o/n"); reason != "main の CI が失敗" {
		t.Fatalf("reason=%q", reason)
	}
	if err := store.ClearStop("o/n"); err != nil {
		t.Fatal(err)
	}
	if reason, _ := store.Stopped("o/n"); reason != "" {
		t.Fatalf("reason=%q", reason)
	}
}

func TestRecordJSONKeysAreStableForTelemetry(t *testing.T) {
	data, err := json.Marshal(Record{RunID: "r", PR: 1})
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	if err := json.Unmarshal(data, &keys); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"run_id", "repo", "kind", "agent", "model", "effort", "status", "repairs", "pr", "first_round_passed", "started_at", "updated_at"} {
		if _, ok := keys[key]; !ok {
			t.Errorf("missing key %s", key)
		}
	}
}

func TestRepoKeysIgnoreCase(t *testing.T) {
	store := Store{Root: t.TempDir()}
	if err := store.SetStop("Owner/Name", "stop"); err != nil {
		t.Fatal(err)
	}
	if reason, _ := store.Stopped("owner/name"); reason != "stop" {
		t.Fatalf("reason=%q", reason)
	}
	if _, err := store.Lock("Owner/Name"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Lock("owner/NAME"); err == nil {
		t.Fatal("lock must not depend on case")
	}
}

func TestRecordKeepsThreadID(t *testing.T) {
	store := Store{Root: t.TempDir()}
	rec := &Record{RunID: "r1", Repo: "o/n", Status: StatusImplementing, ThreadID: "thread-1"}
	if err := store.Save(rec); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("r1")
	if err != nil || loaded.ThreadID != "thread-1" {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}
