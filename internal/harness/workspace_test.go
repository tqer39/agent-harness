//go:build !windows

package harness

import (
	"context"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// sandboxlessRunner は codex sandbox を外して実コマンドを実行する。
type sandboxlessRunner struct{ inner OSRunner }

func (r sandboxlessRunner) Run(ctx context.Context, c Cmd) (string, error) {
	if c.Name == "codex" {
		separator := slices.Index(c.Args, "--")
		directory := c.Args[slices.Index(c.Args, "-C")+1]
		rest := c.Args[separator+1:]
		return r.inner.Run(ctx, Cmd{Dir: directory, Name: rest[0], Args: rest[1:], Env: c.Env})
	}
	return r.inner.Run(ctx, c)
}

type staticToken struct{}

func (staticToken) Token(context.Context) (string, error) { return "unused", nil }

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newRemote(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "init", "-q", "--bare", "-b", "main", bare)
	gitIn(t, root, "init", "-q", "-b", "main", seed)
	if err := os.WriteFile(filepath.Join(seed, "app.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, seed, "add", "-A")
	gitIn(t, seed, "commit", "-q", "-m", "init")
	gitIn(t, seed, "remote", "add", "origin", bare)
	gitIn(t, seed, "push", "-q", "origin", "main")
	return bare
}

func newTree(t *testing.T, remote string) (*GitTree, *Record) {
	t.Helper()
	root := t.TempDir()
	tree := &GitTree{
		Run:        sandboxlessRunner{},
		Tokens:     staticToken{},
		Codex:      "codex",
		StateRoot:  filepath.Join(root, "state"),
		WorkRoot:   filepath.Join(root, "work"),
		RemoteURL:  func(string) string { return remote },
		AuthHeader: false,
	}
	return tree, &Record{RunID: "r1", Repo: "o/n", Base: "main", Title: "feat: x"}
}

func TestPrepareExportApplyCommitPush(t *testing.T) {
	remote := newRemote(t)
	tree, rec := newTree(t, remote)
	ctx := context.Background()
	paths, err := tree.Prepare(ctx, rec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out := gitIn(t, paths.Work, "remote"); out != "" {
		t.Fatalf("work clone keeps remote: %q", out)
	}
	if out := gitIn(t, paths.Trusted, "config", "core.hooksPath"); out != "/dev/null" {
		t.Fatalf("hooksPath=%q", out)
	}
	// 実装担当の変更を模擬する。untracked のファイルと結果ファイルを含む。
	if err := os.WriteFile(filepath.Join(paths.Work, "new.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(paths.Work, ".agent-run"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.Work, ".agent-run", "result.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch, err := tree.Export(ctx, paths.Work)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patch), "new.txt") || strings.Contains(string(patch), "result.json") {
		t.Fatalf("patch=%s", patch)
	}
	if err := tree.Apply(ctx, rec, patch); err != nil {
		t.Fatal(err)
	}
	changes, err := tree.Changes(ctx, rec, rec.BaseSHA)
	if err != nil || len(changes) != 1 || changes[0].Path != "new.txt" {
		t.Fatalf("changes=%+v err=%v", changes, err)
	}
	sha, err := tree.Commit(ctx, rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := tree.Push(ctx, rec, sha); err != nil {
		t.Fatal(err)
	}
	rec.HeadSHA = sha
	if got := gitIn(t, remote, "rev-parse", "refs/heads/agent/r1"); got != sha {
		t.Fatalf("remote=%s sha=%s", got, sha)
	}
	// 同じ差分を再度適用しても、新しいコミットを作らない。
	if err := tree.Apply(ctx, rec, patch); err != nil {
		t.Fatal(err)
	}
	again, err := tree.Commit(ctx, rec)
	if err != nil || again != sha {
		t.Fatalf("again=%s sha=%s err=%v", again, sha, err)
	}
}

func TestExportIgnoresGitHooksFromWorkClone(t *testing.T) {
	remote := newRemote(t)
	tree, rec := newTree(t, remote)
	ctx := context.Background()
	paths, err := tree.Prepare(ctx, rec, nil)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(paths.Trusted, "hook-ran")
	hook := "#!/bin/sh\ntouch " + marker + "\n"
	if err := os.WriteFile(filepath.Join(paths.Work, ".git", "hooks", "pre-commit"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.Work, "app.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch, err := tree.Export(ctx, paths.Work)
	if err != nil {
		t.Fatal(err)
	}
	if err := tree.Apply(ctx, rec, patch); err != nil {
		t.Fatal(err)
	}
	if _, err := tree.Commit(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("hook from the work clone was executed")
	}
}

func TestSandboxEnvKeepsGoModulesInWorkClone(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	env := strings.Join(sandboxEnv("/work"), "\n")
	for _, want := range []string{
		"GOPATH=/work/.agent-run/cache/gopath",
		"GOFLAGS=-modcacherw",
		"GOPROXY=file:///home/u/go/pkg/mod/cache/download|https://proxy.golang.org",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("missing %q in\n%s", want, env)
		}
	}
}

// outputSize は受け取った出力の最大長を記録する。
type outputSize struct {
	inner   Runner
	largest int
}

func (r *outputSize) Run(ctx context.Context, c Cmd) (string, error) {
	out, err := r.inner.Run(ctx, c)
	r.largest = max(r.largest, len(out))
	return out, err
}

func TestExportStopsReadingAtLimit(t *testing.T) {
	tree, rec := newTree(t, newRemote(t))
	ctx := context.Background()
	paths, err := tree.Prepare(ctx, rec, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 生成物を大量に作ると、差分全体をメモリに読み込んでから上限を確認することになる。
	data := make([]byte, 2*maxPatchBytes)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.Work, "big.bin"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	size := &outputSize{inner: tree.Run}
	tree.Run = size
	if _, err := tree.Export(ctx, paths.Work); err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("err=%v", err)
	}
	if size.largest > maxPatchBytes+1 {
		t.Fatalf("read %d bytes", size.largest)
	}
}

func TestApplyRejectsEmptyPatch(t *testing.T) {
	tree, rec := newTree(t, newRemote(t))
	ctx := context.Background()
	if _, err := tree.Prepare(ctx, rec, nil); err != nil {
		t.Fatal(err)
	}
	if err := tree.Apply(ctx, rec, nil); err == nil {
		t.Fatal("empty patch was accepted")
	}
}

func TestPrepareAppliesAcceptancePatchAndPins(t *testing.T) {
	remote := newRemote(t)
	tree, rec := newTree(t, remote)
	patch := "diff --git a/tests/acceptance.txt b/tests/acceptance.txt\nnew file mode 100644\n--- /dev/null\n+++ b/tests/acceptance.txt\n@@ -0,0 +1 @@\n+must pass\n"
	paths, err := tree.Prepare(context.Background(), rec, []byte(patch))
	if err != nil {
		t.Fatal(err)
	}
	if rec.AcceptanceSHA == rec.BaseSHA || !slices.Contains(rec.Pinned, "tests/acceptance.txt") {
		t.Fatalf("rec=%+v", rec)
	}
	if _, err := os.Stat(filepath.Join(paths.Work, "tests", "acceptance.txt")); err != nil {
		t.Fatalf("work clone has no acceptance test: %v", err)
	}
}

func TestRevertCreatesBranch(t *testing.T) {
	remote := newRemote(t)
	tree, rec := newTree(t, remote)
	ctx := context.Background()
	paths, err := tree.Prepare(ctx, rec, nil)
	if err != nil {
		t.Fatal(err)
	}
	seed := filepath.Join(filepath.Dir(remote), "seed")
	if err := os.WriteFile(filepath.Join(seed, "app.txt"), []byte("bad\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, seed, "add", "-A")
	gitIn(t, seed, "commit", "-q", "-m", "bad")
	gitIn(t, seed, "push", "-q", "origin", "main")
	rec.MergeSHA = gitIn(t, seed, "rev-parse", "HEAD")
	branch, sha, err := tree.Revert(ctx, rec)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "agent/revert-r1" || gitIn(t, remote, "rev-parse", "refs/heads/"+branch) != sha {
		t.Fatalf("branch=%s sha=%s", branch, sha)
	}
	if got := gitIn(t, paths.Trusted, "show", sha+":app.txt"); got != "one" {
		t.Fatalf("app.txt=%q", got)
	}
}

func TestPushVerifiesRemoteWhenReusingSHA(t *testing.T) {
	remote := newRemote(t)
	tree, rec := newTree(t, remote)
	ctx := context.Background()
	paths, err := tree.Prepare(ctx, rec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.Work, "new.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch, err := tree.Export(ctx, paths.Work)
	if err != nil {
		t.Fatal(err)
	}
	if err := tree.Apply(ctx, rec, patch); err != nil {
		t.Fatal(err)
	}
	sha, err := tree.Commit(ctx, rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := tree.Push(ctx, rec, sha); err != nil {
		t.Fatal(err)
	}
	rec.HeadSHA = sha
	if err := tree.Push(ctx, rec, sha); err != nil {
		t.Fatalf("unchanged remote: %v", err)
	}
	gitIn(t, remote, "update-ref", "refs/heads/agent/r1", rec.BaseSHA)
	if err := tree.Push(ctx, rec, sha); err == nil {
		t.Fatal("changed remote branch was ignored")
	}
}

func TestRevertCanBeRepeatedAfterMainMoves(t *testing.T) {
	remote := newRemote(t)
	tree, rec := newTree(t, remote)
	ctx := context.Background()
	if _, err := tree.Prepare(ctx, rec, nil); err != nil {
		t.Fatal(err)
	}
	seed := filepath.Join(filepath.Dir(remote), "seed")
	if err := os.WriteFile(filepath.Join(seed, "app.txt"), []byte("bad\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, seed, "add", "-A")
	gitIn(t, seed, "commit", "-q", "-m", "bad")
	gitIn(t, seed, "push", "-q", "origin", "main")
	rec.MergeSHA = gitIn(t, seed, "rev-parse", "HEAD")
	if _, _, err := tree.Revert(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "other.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, seed, "add", "-A")
	gitIn(t, seed, "commit", "-q", "-m", "other")
	gitIn(t, seed, "push", "-q", "origin", "main")
	branch, sha, err := tree.Revert(ctx, rec)
	if err != nil {
		t.Fatalf("second revert: %v", err)
	}
	if gitIn(t, remote, "rev-parse", "refs/heads/"+branch) != sha {
		t.Fatal("branch was not updated")
	}
}

func TestSandboxEnvKeepsMiseAndUVStateInWorkClone(t *testing.T) {
	env := sandboxEnv("/work")
	for _, want := range []string{
		"MISE_STATE_DIR=/work/.agent-run/cache/mise-state",
		"MISE_CACHE_DIR=/work/.agent-run/cache/mise",
		"UV_CACHE_DIR=/work/.agent-run/cache/uv",
	} {
		if !slices.Contains(env, want) {
			t.Errorf("missing %q in %v", want, env)
		}
	}
	// 信頼する範囲を広げない。作業用 clone だけを指定する。
	if !slices.Contains(env, "MISE_TRUSTED_CONFIG_PATHS=/work") {
		t.Errorf("trusted paths must be only the work clone: %v", env)
	}
}

func TestImplementerAndVerifyShareMiseState(t *testing.T) {
	runner := &scriptRunner{}
	tree := &GitTree{Run: runner, Codex: "codex"}
	if _, _, err := tree.Verify(context.Background(), "/work", []string{"mise", "run", "check"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(runner.calls[0].Env, "MISE_STATE_DIR=/work/.agent-run/cache/mise-state") {
		t.Fatalf("verify env=%v", runner.calls[0].Env)
	}
	args := (&Herdr{Codex: "codex"}).implementerArgs(&Record{RunID: "r1", Model: "m", Effort: "low"}, "/work", "p", nil)
	if !slices.Contains(args, "MISE_STATE_DIR=/work/.agent-run/cache/mise-state") {
		t.Fatalf("implementer args=%v", args)
	}
}

func TestPrepareCopiesStandardsRuntimeWithoutExportingIt(t *testing.T) {
	remote := newRemote(t)
	tree, rec := newTree(t, remote)
	source := filepath.Join(tree.WorkRoot, "n", ".standards", "cache", "dev-standards")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	paths, err := tree.Prepare(context.Background(), rec, nil)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(paths.Work, ".standards", "cache", "dev-standards"))
	if err != nil || info.Mode()&0o100 == 0 {
		t.Fatalf("runtime was not copied as an executable: info=%v err=%v", info, err)
	}
	// 利用先が .standards/cache/ を無視していなくても、差分に含めない。
	if err := os.WriteFile(filepath.Join(paths.Work, "new.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch, err := tree.Export(context.Background(), paths.Work)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(patch), "dev-standards") {
		t.Fatalf("patch includes the runtime: %s", patch)
	}
}

func TestPrepareWithoutStandardsRuntime(t *testing.T) {
	tree, rec := newTree(t, newRemote(t))
	paths, err := tree.Prepare(context.Background(), rec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(paths.Work, ".standards")); !os.IsNotExist(err) {
		t.Fatalf(".standards was created without a runtime: %v", err)
	}
}

func TestCopyStandardsRuntimeRefusesSymlink(t *testing.T) {
	tree, rec := newTree(t, "")
	source := filepath.Join(tree.WorkRoot, "n", ".standards", "cache", "dev-standards")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(work, ".standards")); err != nil {
		t.Fatal(err)
	}
	if err := tree.copyStandardsRuntime(rec, work); err == nil {
		t.Fatal("runtime was written through a symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "cache")); !os.IsNotExist(err) {
		t.Fatalf("wrote outside the clone: %v", err)
	}
}

func TestCopyStandardsRuntimeKeepsExistingTarget(t *testing.T) {
	tree, rec := newTree(t, "")
	source := filepath.Join(tree.WorkRoot, "n", ".standards", "cache", "dev-standards")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	cache := filepath.Join(work, ".standards", "cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	// 実行ファイルの位置が clone の外へのリンクでも、リンク先を書き換えない。
	outside := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cache, "dev-standards")); err != nil {
		t.Fatal(err)
	}
	if err := tree.copyStandardsRuntime(rec, work); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(outside); string(data) != "keep" {
		t.Fatalf("file outside the clone was overwritten: %q", data)
	}
}

func TestCopyStandardsRuntimeIgnoresMissingCheckoutDirectory(t *testing.T) {
	tree, rec := newTree(t, "")
	// 利用者の checkout の位置がファイルだと ENOTDIR になる。実行ファイルが無いのと同じに扱う。
	if err := os.MkdirAll(tree.WorkRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree.WorkRoot, "n"), []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tree.copyStandardsRuntime(rec, t.TempDir()); err != nil {
		t.Fatal(err)
	}
}
