//go:build !windows

package harness

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

var bundlePattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

const maxPatchBytes = 5 << 20

// ErrNoChanges は実装担当の差分がないことを表す。
var ErrNoChanges = errors.New("実装担当の差分がありません")

type Paths struct {
	Trusted string
	Work    string
	Review  string
}

// GitTree は専用 clone を管理する。作業用 clone の Git はサンドボックス内でだけ実行する。
type GitTree struct {
	Run        Runner
	Tokens     TokenSource
	Codex      string
	StateRoot  string
	WorkRoot   string
	RemoteURL  func(repo string) string
	AuthHeader bool
}

func (t *GitTree) PathsFor(rec *Record) Paths {
	base := filepath.Join(t.StateRoot, "runs", rec.RunID+".d")
	_, name, _ := strings.Cut(rec.Repo, "/")
	return Paths{
		Trusted: filepath.Join(base, "clone"),
		Work:    filepath.Join(t.WorkRoot, name+"-worktrees", "agent-"+rec.RunID),
		Review:  filepath.Join(base, "review-"+rec.RunID),
	}
}

func (t *GitTree) authEnv(ctx context.Context) ([]string, []string, error) {
	if !t.AuthHeader {
		return nil, nil, nil
	}
	token, err := t.Tokens.Token(ctx)
	if err != nil {
		return nil, nil, err
	}
	encoded := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	// 引数に出さず環境変数で渡し、プロセス一覧と例外メッセージに残さない。
	env := []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.https://github.com/.extraheader",
		"GIT_CONFIG_VALUE_0=AUTHORIZATION: basic " + encoded,
	}
	return env, []string{token, encoded}, nil
}

func (t *GitTree) git(ctx context.Context, dir, stdin string, args ...string) (string, error) {
	full := append([]string{"-c", "user.name=agent-harness[bot]", "-c", "user.email=agent-harness@users.noreply.github.com"}, args...)
	return t.Run.Run(ctx, Cmd{Dir: dir, Name: "git", Args: full, Stdin: stdin})
}

func (t *GitTree) authGit(ctx context.Context, dir string, args ...string) (string, error) {
	env, secrets, err := t.authEnv(ctx)
	if err != nil {
		return "", err
	}
	return t.Run.Run(ctx, Cmd{Dir: dir, Name: "git", Args: args, Env: env, Secrets: secrets})
}

// Prepare は専用 clone、作業用 clone、レビュー用 checkout を作る。
func (t *GitTree) Prepare(ctx context.Context, rec *Record, acceptance []byte) (Paths, error) {
	paths := t.PathsFor(rec)
	if err := os.MkdirAll(filepath.Dir(paths.Trusted), 0o700); err != nil {
		return paths, err
	}
	if err := os.MkdirAll(filepath.Dir(paths.Work), 0o755); err != nil {
		return paths, err
	}
	if _, err := t.authGit(ctx, "", "clone", "--branch", rec.Base, t.RemoteURL(rec.Repo), paths.Trusted); err != nil {
		return paths, err
	}
	if _, err := t.git(ctx, paths.Trusted, "", "config", "core.hooksPath", "/dev/null"); err != nil {
		return paths, err
	}
	head, err := t.git(ctx, paths.Trusted, "", "rev-parse", "HEAD")
	if err != nil {
		return paths, err
	}
	rec.BaseSHA = strings.TrimSpace(head)
	rec.AcceptanceSHA = rec.BaseSHA
	if _, err := t.git(ctx, paths.Trusted, "", "worktree", "add", "--detach", paths.Review, rec.BaseSHA); err != nil {
		return paths, err
	}
	if len(acceptance) > 0 {
		if _, err := t.git(ctx, paths.Trusted, string(acceptance), "apply", "--index", "--binary", "-"); err != nil {
			return paths, fmt.Errorf("受入テストを適用できません: %w", err)
		}
		raw, err := t.git(ctx, paths.Trusted, "", "diff", "--cached", "--raw", "--no-renames", "-z", "HEAD")
		if err != nil {
			return paths, err
		}
		changes, err := ParseRaw(raw)
		if err != nil {
			return paths, err
		}
		for _, change := range changes {
			rec.Pinned = append(rec.Pinned, change.Path)
		}
		if _, err := t.git(ctx, paths.Trusted, "", "commit", "-q", "-m", "test: 受入テストを固定"); err != nil {
			return paths, err
		}
		head, err := t.git(ctx, paths.Trusted, "", "rev-parse", "HEAD")
		if err != nil {
			return paths, err
		}
		rec.AcceptanceSHA = strings.TrimSpace(head)
	}
	// 認証情報を持たないよう、専用 clone から複製して接続先も消す。
	if _, err := t.git(ctx, "", "", "clone", "--no-hardlinks", "--quiet", paths.Trusted, paths.Work); err != nil {
		return paths, err
	}
	if _, err := t.git(ctx, paths.Work, "", "remote", "remove", "origin"); err != nil {
		return paths, err
	}
	exclude := filepath.Join(paths.Work, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(exclude), 0o755); err != nil {
		return paths, err
	}
	// 共通ルールの実行ファイルは Git 管理外なので、差分に含めないよう除外する。
	if err := os.WriteFile(exclude, []byte(".agent-run/\n.standards/cache/\n"), 0o644); err != nil {
		return paths, err
	}
	if err := t.copyStandardsRuntime(rec, paths.Work); err != nil {
		return paths, err
	}
	return paths, nil
}

// copyStandardsRuntime は利用者の checkout にある dev-standards の実行ファイルを作業用 clone に置く。
// Git 管理外なので clone には無く、mise run check や standards:prepare が失敗する。
// サンドボックスからは GitHub に接続できないので、作業用 clone の中では取得できない。
func (t *GitTree) copyStandardsRuntime(rec *Record, work string) error {
	_, name, _ := strings.Cut(rec.Repo, "/")
	checkout := filepath.Join(t.WorkRoot, name)
	data, err := os.ReadFile(filepath.Join(checkout, ".standards", "cache", "dev-standards"))
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		// dev-standards を採用していないリポジトリでは何もしない。
		return nil
	}
	if err != nil {
		return err
	}
	// 基準ブランチの内容で .standards がリンクにされていると、clone の外に書き込んでしまう。
	for _, dir := range []string{".standards", filepath.Join(".standards", "cache")} {
		info, err := os.Lstat(filepath.Join(work, dir))
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s がシンボリックリンクなので dev-standards を配置しません", dir)
		}
	}
	if runtime := lockedRuntime(checkout, work); runtime != "" {
		if data, err = os.ReadFile(runtime); err != nil {
			return err
		}
	}
	target := filepath.Join(work, ".standards", "cache", "dev-standards")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	// 既にあるもの（Git 管理のファイルやリンク）は上書きしない。O_EXCL はリンクの先も辿らない。
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

// lockedRuntime は clone の lock.json が記録した採用版の実行ファイルを、保守ホストの状態から探す。
// checkout のキャッシュの CLI は host.json を見て採用版に処理を任せるが、clone には host.json を置かない。
// キャッシュの CLI をそのまま置くと、採用版と生成結果が食い違い、検査が設定の不整合で失敗する。
// 見つからなければ空を返し、呼び出し側はキャッシュの CLI を使う。
func lockedRuntime(checkout, work string) string {
	var host struct {
		State string `json:"state"`
	}
	data, err := os.ReadFile(filepath.Join(checkout, ".standards", "cache", "host.json"))
	if err != nil || json.Unmarshal(data, &host) != nil || host.State == "" {
		return ""
	}
	var lock struct {
		Bundle string `json:"bundleSha256"`
	}
	data, err = os.ReadFile(filepath.Join(work, ".standards", "lock.json"))
	// 値は clone の内容から来るので、パスに使う前に形式を確かめる。
	if err != nil || json.Unmarshal(data, &lock) != nil || !bundlePattern.MatchString(lock.Bundle) {
		return ""
	}
	runtime := filepath.Join(host.State, "bundles", lock.Bundle, "dev-standards")
	if info, err := os.Stat(runtime); err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return runtime
}

func (t *GitTree) sandbox(ctx context.Context, dir string, env []string, command ...string) (string, error) {
	args := append([]string{"-p", "agent-harness", "sandbox", "-P", "agent-harness", "-C", dir, "--"}, command...)
	return t.Run.Run(ctx, Cmd{Name: t.Codex, Args: args, Env: env})
}

// sandboxEnv はサンドボックス内のコマンドの書き込み先を作業用 clone に向ける。HOME の下には書けない。
func sandboxEnv(dir string) []string {
	cache := filepath.Join(dir, ".agent-run", "cache")
	return []string{
		"MISE_YES=1",
		// 信頼するのはハーネスが作った作業用 clone だけにする。
		"MISE_TRUSTED_CONFIG_PATHS=" + dir,
		// mise は信頼済みの設定でも記録を状態ディレクトリに書く。既定の ~/.local/state/mise には書けず、
		// 設定を読めずに失敗するので、状態とキャッシュも作業用 clone に置く。
		"MISE_STATE_DIR=" + filepath.Join(cache, "mise-state"),
		"MISE_CACHE_DIR=" + filepath.Join(cache, "mise"),
		"XDG_CACHE_HOME=" + cache,
		"GOCACHE=" + filepath.Join(cache, "go"),
		// 既定の ~/go/pkg/mod には書けず、依存を追加できない。チェックサムのキャッシュも GOPATH の下に置かれる。
		"GOPATH=" + filepath.Join(cache, "gopath"),
		// 読み取り専用で作られると、作業用 clone を削除できない。
		"GOFLAGS=-modcacherw",
		"GOPROXY=" + goProxy(),
		"UV_CACHE_DIR=" + filepath.Join(cache, "uv"),
		"npm_config_cache=" + filepath.Join(cache, "npm"),
	}
}

// goProxy は既存のモジュールキャッシュを読み取り専用で先に使う。無いものは許可済みの proxy.golang.org から取る。
// 404 以外の読み取りエラーでもモジュールの取得が次に進むように、"," ではなく "|" で区切る。
func goProxy() string {
	const proxy = "https://proxy.golang.org"
	home, err := os.UserHomeDir()
	if err != nil {
		return proxy
	}
	return "file://" + filepath.Join(home, "go", "pkg", "mod", "cache", "download") + "|" + proxy
}

// Verify はリポジトリの検証をサンドボックス内で実行する。
func (t *GitTree) Verify(ctx context.Context, work string, command []string) (bool, string, error) {
	out, err := t.sandbox(ctx, work, sandboxEnv(work), command...)
	var runErr *RunError
	if errors.As(err, &runErr) {
		return false, tail(out+runErr.Stderr, 8000), nil
	}
	return err == nil, tail(out, 8000), err
}

const exportScript = `set -eu
mkdir -p .agent-run/objects
export GIT_INDEX_FILE=.agent-run/index
export GIT_OBJECT_DIRECTORY="$PWD/.agent-run/objects"
export GIT_ALTERNATE_OBJECT_DIRECTORIES="$PWD/.git/objects"
git read-tree HEAD
git add -A
# 上限を超えた分は読み込まない。1 バイト多く出し、超えたことを呼び出し側で判定する。
git diff --cached --binary --no-renames HEAD | head -c %d`

// Export は差分をサンドボックス内で出力する。インデックスとオブジェクトは .agent-run に置く。
func (t *GitTree) Export(ctx context.Context, work string) ([]byte, error) {
	out, err := t.sandbox(ctx, work, nil, "sh", "-c", fmt.Sprintf(exportScript, maxPatchBytes+1))
	if err != nil {
		return nil, err
	}
	if len(out) > maxPatchBytes {
		return nil, fmt.Errorf("差分が上限 %d バイトを超えています", maxPatchBytes)
	}
	return []byte(out), nil
}

// Apply は受入テスト適用後の状態に戻してから、差分全体を適用する。
func (t *GitTree) Apply(ctx context.Context, rec *Record, patch []byte) error {
	if len(strings.TrimSpace(string(patch))) == 0 {
		return ErrNoChanges
	}
	trusted := t.PathsFor(rec).Trusted
	// 前回のステージ内容が checkout を妨げないよう、先に破棄する。
	if _, err := t.git(ctx, trusted, "", "reset", "--quiet", "--hard"); err != nil {
		return err
	}
	if _, err := t.git(ctx, trusted, "", "checkout", "--quiet", "--detach", rec.AcceptanceSHA); err != nil {
		return err
	}
	if _, err := t.git(ctx, trusted, "", "clean", "-q", "-fdx"); err != nil {
		return err
	}
	_, err := t.git(ctx, trusted, string(patch), "apply", "--index", "--binary", "-")
	return err
}

// Changes は from からステージ済みの状態までの変更を返す。
func (t *GitTree) Changes(ctx context.Context, rec *Record, from string) ([]Change, error) {
	raw, err := t.git(ctx, t.PathsFor(rec).Trusted, "", "diff", "--cached", "--raw", "--no-renames", "-z", from)
	if err != nil {
		return nil, err
	}
	return ParseRaw(raw)
}

// StagedPatch はレビュー用の差分を基準コミットから出力する。
func (t *GitTree) StagedPatch(ctx context.Context, rec *Record) (string, error) {
	return t.git(ctx, t.PathsFor(rec).Trusted, "", "diff", "--no-renames", rec.BaseSHA, rec.HeadSHA)
}

// Commit はステージ済みの内容をコミットする。既存の HEAD と同じ内容なら再利用する。
func (t *GitTree) Commit(ctx context.Context, rec *Record) (string, error) {
	trusted := t.PathsFor(rec).Trusted
	tree, err := t.git(ctx, trusted, "", "write-tree")
	if err != nil {
		return "", err
	}
	if rec.HeadSHA != "" {
		existing, err := t.git(ctx, trusted, "", "rev-parse", rec.HeadSHA+"^{tree}")
		if err == nil && strings.TrimSpace(existing) == strings.TrimSpace(tree) {
			return rec.HeadSHA, nil
		}
	}
	if _, err := t.git(ctx, trusted, "", "commit", "-q", "-m", rec.Title); err != nil {
		return "", err
	}
	head, err := t.git(ctx, trusted, "", "rev-parse", "HEAD")
	return strings.TrimSpace(head), err
}

// Push はハーネス専用ブランチを更新する。他者の更新は lease で検出する。
func (t *GitTree) Push(ctx context.Context, rec *Record, sha string) error {
	ref := "refs/heads/agent/" + rec.RunID
	if sha == rec.HeadSHA {
		// 差分が同じでも、ブランチが変更されていれば以前の検証結果は使えない。
		out, err := t.authGit(ctx, t.PathsFor(rec).Trusted, "ls-remote", "origin", ref)
		if err != nil {
			return err
		}
		fields := strings.Fields(out)
		if len(fields) == 0 || fields[0] != sha {
			return fmt.Errorf("%s の先頭が変更されています", ref)
		}
		return nil
	}
	_, err := t.authGit(ctx, t.PathsFor(rec).Trusted, "push",
		"--force-with-lease="+ref+":"+rec.HeadSHA, "origin", sha+":"+ref)
	return err
}

// Revert はマージ済みのコミットを打ち消すブランチを作る。
func (t *GitTree) Revert(ctx context.Context, rec *Record) (string, string, error) {
	trusted := t.PathsFor(rec).Trusted
	if _, err := t.authGit(ctx, trusted, "fetch", "--quiet", "origin", rec.Base); err != nil {
		return "", "", err
	}
	if _, err := t.git(ctx, trusted, "", "reset", "--quiet", "--hard"); err != nil {
		return "", "", err
	}
	if _, err := t.git(ctx, trusted, "", "checkout", "--quiet", "--detach", "FETCH_HEAD"); err != nil {
		return "", "", err
	}
	if _, err := t.git(ctx, trusted, "", "revert", "--no-edit", rec.MergeSHA); err != nil {
		return "", "", err
	}
	head, err := t.git(ctx, trusted, "", "rev-parse", "HEAD")
	if err != nil {
		return "", "", err
	}
	sha := strings.TrimSpace(head)
	branch := "agent/revert-" + rec.RunID
	// ハーネス専用のブランチなので、中断後の再実行では作り直す。
	if _, err := t.authGit(ctx, trusted, "push", "origin", "+"+sha+":refs/heads/"+branch); err != nil {
		return "", "", err
	}
	return branch, sha, nil
}

func tail(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[len(text)-limit:]
}
