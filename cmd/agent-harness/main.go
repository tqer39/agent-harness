//go:build !windows

// agent-harness は受入条件の承認済みタスクを無人で処理する。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	harnessassets "github.com/tqer39/agent-harness/harness"
	"github.com/tqer39/agent-harness/internal/harness"
)

// betterleaksTool は mise で起動する版を固定する。agent-telemetry の版と揃える。
const betterleaksTool = "aqua:betterleaks/betterleaks@1.9.0"

// defaultModel は実装担当の既定のモデルである。使えるかは run と doctor で確認する。
const defaultModel = "gpt-6.1-sol"

type runProfile struct{ Agent, Model, Effort, Reviewer string }

func profile(agent, model, effort string) (runProfile, error) {
	// Claude を実装担当にする分離は未検証なので、確認できるまで受け付けない。
	if agent != "codex" {
		return runProfile{}, fmt.Errorf("実装担当 %q はまだ使えません", agent)
	}
	if !slices.Contains([]string{"low", "medium", "high"}, effort) {
		return runProfile{}, fmt.Errorf("エフォート %q は使えません", effort)
	}
	return runProfile{Agent: agent, Model: model, Effort: effort, Reviewer: "claude-opus-5-5"}, nil
}

func main() {
	if err := dispatch(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		switch {
		case errors.Is(err, harness.ErrQuota):
			os.Exit(3)
		case errors.Is(err, harness.ErrStopped):
			os.Exit(4)
		}
		os.Exit(1)
	}
}

func dispatch(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("使い方: agent-harness <setup|setup-api-key|doctor|run|resume|reconcile|clear-stop>")
	}
	switch args[0] {
	case "setup":
		return installProfile()
	case "setup-api-key":
		return setupAPIKey(out)
	case "doctor":
		return doctor(args[1:], out)
	case "run":
		return runCommand(args[1:], out)
	case "resume":
		return resumeCommand(args[1:], out)
	case "reconcile":
		return reconcileCommand(args[1:], out)
	case "clear-stop":
		return clearStop(args[1:], out)
	}
	return fmt.Errorf("不明なコマンドです: %s", args[0])
}

func stateRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "agent-harness"), nil
}

// codexHomeFor は認証方式に対応する Codex ホームを返す。空は既定の ~/.codex を表す。
func codexHomeFor(auth string) (string, error) {
	switch auth {
	case "", "chatgpt":
		return "", nil
	case "api-key":
		root, err := stateRoot()
		if err != nil {
			return "", err
		}
		return filepath.Join(root, "codex-api-home"), nil
	}
	return "", fmt.Errorf("Codex の認証方式 %q は使えません", auth)
}

func setupAPIKey(out io.Writer) error {
	home, err := codexHomeFor("api-key")
	if err != nil {
		return err
	}
	codex, err := resolve("codex")
	if err != nil {
		return err
	}
	runner := harness.OSRunner{}
	if err := harness.SetupAPIKey(context.Background(), runner, harness.OPSecrets{Run: runner}, codex, home, harnessassets.CodexProfile); err != nil {
		return err
	}
	if err := harness.APIKeyLoggedIn(context.Background(), runner, codex, home); err != nil {
		return err
	}
	fmt.Fprintf(out, "API キーを %s に登録しました。~/.codex は変更していません。\n", home)
	return nil
}

// workRoot はハーネスが作業用 clone を作る場所である。
func workRoot(home string) string { return filepath.Join(home, "workspace", "tqer39") }

func installProfile() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".codex", "agent-harness.config.toml")
	existing, err := os.ReadFile(path)
	if err == nil {
		if harness.ProfileMatches(existing, harnessassets.CodexProfile, workRoot(home)) {
			return nil
		}
		return fmt.Errorf("%s は内容が異なるので上書きしません。新しい版を導入するには、内容を確認してから `mv %s %s.bak` で退避し、`agent-harness setup` を再実行してください", path, path, path)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, harnessassets.CodexProfile, 0o600)
}

func resolve(name string) (string, error) {
	// 実行時の PATH に左右されないよう、絶対パスで渡す。
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s が見つかりません: %w", name, err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return realPath(path, name, miseWhich)
}

func miseWhich(name string) (string, error) {
	// 止まると run や doctor が待ち続けるので、上限を設ける。
	return commandOutput(30*time.Second, "mise", "which", name)
}

func commandOutput(limit time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	// 子の子が出力を開いたまま残ると Wait が戻らないので、読み取りの待機も打ち切る。
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	result := strings.TrimSpace(string(out))
	if err != nil && ctx.Err() != nil {
		return "", fmt.Errorf("%s が %s 以内に終わりません: %w", name, limit, ctx.Err())
	}
	// ErrWaitDelay は正常に終わった後に子の子が出力を開いていただけなので、得た出力を使う。
	if errors.Is(err, exec.ErrWaitDelay) && result != "" {
		return result, nil
	}
	if err != nil {
		return "", err
	}
	return result, nil
}

// realPath は mise の shim を実体に解決する。shim のまま渡すと、作業用 clone の mise の設定で
// 別の版に切り替わることがある。解決できなければ別の版で動かさず失敗させる。
func realPath(path, name string, which func(string) (string, error)) (string, error) {
	if !strings.Contains(path, "/mise/shims/") {
		return path, nil
	}
	real, err := which(name)
	if err != nil || real == "" {
		return "", fmt.Errorf("mise の shim から %s の実体を解決できません: %v", name, err)
	}
	return real, nil
}

func build(repo, codexAuth string, out io.Writer) (*harness.Orchestrator, error) {
	codexHome, err := codexHomeFor(codexAuth)
	if err != nil {
		return nil, err
	}
	root, err := stateRoot()
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	paths := map[string]string{}
	for _, name := range []string{"codex", "claude", "op", "gh", "git", "mise"} {
		path, err := resolve(name)
		if err != nil {
			return nil, err
		}
		paths[name] = path
	}
	runner := harness.OSRunner{}
	tokens := &harness.AppTokens{
		Secrets: harness.OPSecrets{Run: runner},
		Repo:    repo,
		API:     harness.GitHubAPI,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
		Now:     time.Now,
	}
	tree := &harness.GitTree{
		Run:        runner,
		Tokens:     tokens,
		Codex:      paths["codex"],
		StateRoot:  root,
		WorkRoot:   workRoot(home),
		RemoteURL:  func(repo string) string { return "https://github.com/" + repo + ".git" },
		AuthHeader: true,
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	store := harness.Store{Root: root}
	return &harness.Orchestrator{
		Store: store,
		Agents: &harness.CodexExec{
			Run:       runner,
			Codex:     paths["codex"],
			Claude:    paths["claude"],
			CodexHome: codexHome,
			Events:    filepath.Join(root, "runs"),
			Out:       out,
			Save:      store.Save,
		},
		Tree:          tree,
		Forge:         &harness.GitHub{Run: runner, Tokens: tokens, Repo: repo, Poll: 15 * time.Second},
		Scanner:       scanner(runner, paths["mise"], root),
		Config:        harness.LoadRepoConfig,
		RoundLimit:    30 * time.Minute,
		MergeLimit:    30 * time.Minute,
		ExternalGrace: harness.ExternalReviewGrace,
	}, nil
}

// signalContext はシグナルで取り消される context を返す。
// 子プロセスは別のプロセスグループで動くので、シグナルを context のキャンセルに変えて止める。
func signalContext() (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	// 1 回目のシグナルの後は既定の動作に戻し、2 回目の Ctrl-C で強制終了できるようにする。
	go func() { <-ctx.Done(); stop() }()
	return ctx, stop
}

// scanner は版を固定して betterleaks を起動する。
// mise の shim はプロジェクトの外で版を決められず失敗するので、直接は使わない。
func scanner(runner harness.Runner, mise, dir string) harness.Betterleaks {
	return harness.Betterleaks{Run: runner, Bin: mise, Prefix: []string{"exec", betterleaksTool, "--", "betterleaks"}, Dir: dir}
}

func runCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	repo := flags.String("repo", "", "owner/name")
	base := flags.String("base", "main", "基準ブランチ")
	title := flags.String("title", "", "コミットと PR の題名")
	kind := flags.String("kind", "feature", "feature, bugfix, refactor")
	taskFile := flags.String("task-file", "", "タスクの説明")
	criteriaFile := flags.String("criteria-file", "", "人間が承認した受入条件")
	acceptancePatch := flags.String("acceptance-patch", "", "固定する受入テストの差分")
	approved := flags.Bool("approved-by-human", false, "受入条件を人間が承認した")
	agent := flags.String("agent", "codex", "実装担当")
	model := flags.String("model", defaultModel, "モデル")
	effort := flags.String("effort", "medium", "エフォート")
	codexAuth := flags.String("codex-auth", "chatgpt", "Codex の認証: chatgpt, api-key（従量課金）")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !*approved {
		return errors.New("--approved-by-human がありません。受入条件の承認後に実行してください")
	}
	if *repo == "" || *title == "" {
		return errors.New("--repo と --title は必須です")
	}
	if !slices.Contains([]string{"feature", "bugfix", "refactor"}, *kind) {
		return fmt.Errorf("タスク種別 %q は使えません", *kind)
	}
	selected, err := profile(*agent, *model, *effort)
	if err != nil {
		return err
	}
	task, err := os.ReadFile(*taskFile)
	if err != nil {
		return err
	}
	criteria, err := os.ReadFile(*criteriaFile)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(task)) == "" {
		return errors.New("タスクの説明が空です")
	}
	if strings.TrimSpace(string(criteria)) == "" {
		return errors.New("受入条件が空です")
	}
	var acceptance []byte
	if *acceptancePatch != "" {
		acceptance, err = os.ReadFile(*acceptancePatch)
		if err != nil {
			return err
		}
	}
	if err := updateCodex(out); err != nil {
		return err
	}
	// 使えないモデルでは実装担当が起動直後に止まるので、clone を作る前に確認する。
	if err := checkModel(*codexAuth, selected.Model); err != nil {
		return err
	}
	orchestrator, err := build(*repo, *codexAuth, out)
	if err != nil {
		return err
	}
	rec := &harness.Record{
		CodexAuth: *codexAuth,
		RunID:     harness.NewRunID(time.Now()), Repo: *repo, Base: *base, Title: *title, Kind: *kind,
		Task: strings.TrimSpace(string(task)), Criteria: strings.TrimSpace(string(criteria)),
		Agent: selected.Agent, Model: selected.Model, Effort: selected.Effort, ReviewerModel: selected.Reviewer,
		Status: harness.StatusImplementing,
	}
	fmt.Fprintf(out, "実行 ID: %s\n", rec.RunID)
	ctx, stop := signalContext()
	defer stop()
	err = orchestrator.Run(ctx, rec, acceptance, *approved)
	return report(out, rec, err)
}

// updateCodex は作業の前に Codex CLI を最新の版にする。
func updateCodex(out io.Writer) error {
	mise, err := resolve("mise")
	if err != nil {
		return err
	}
	// OSRunner は環境変数を絞るので、利用者の mise の設定とインストール先を指す値を渡す。
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "MISE_") {
			env = append(env, kv)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	from, to, err := harness.UpdateCodex(ctx, harness.OSRunner{}, mise, env)
	if err != nil {
		// レジストリに届かなくても、今の CLI でモデルを使えるなら続ける。使えるかは checkModel が判定する。
		fmt.Fprintf(out, "Codex CLI を更新できないので、今の版で続けます: %v\n", err)
		return nil
	}
	if to != "" {
		fmt.Fprintf(out, "Codex CLI を %s から %s に更新しました。\n", from, to)
	}
	return nil
}

// checkModel は実行と同じ認証方式と Codex の実体で、モデルを使えるかを確認する。
func checkModel(auth, model string) error {
	home, err := codexHomeFor(auth)
	if err != nil {
		return err
	}
	codex, err := resolve("codex")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return harness.ModelAvailable(ctx, harness.OSRunner{}, codex, home, model)
}

func report(out io.Writer, rec *harness.Record, err error) error {
	fmt.Fprintf(out, "状態: %s\nPR: %d\n", rec.Status, rec.PR)
	if rec.StopReason != "" {
		fmt.Fprintf(out, "停止理由: %s\n", rec.StopReason)
	}
	return err
}

func resumeCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("resume", flag.ContinueOnError)
	id := flags.String("run", "", "実行 ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	root, err := stateRoot()
	if err != nil {
		return err
	}
	rec, err := harness.Store{Root: root}.Load(*id)
	if err != nil {
		return err
	}
	// 再開でも同じ認証を使う。別の方式に切り替えると課金先が変わるためである。
	orchestrator, err := build(rec.Repo, rec.CodexAuth, out)
	if err != nil {
		return err
	}
	ctx, stop := signalContext()
	defer stop()
	return report(out, rec, orchestrator.Resume(ctx, rec))
}

func reconcileCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("reconcile", flag.ContinueOnError)
	repo := flags.String("repo", "", "owner/name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *repo == "" {
		return errors.New("--repo は必須です")
	}
	orchestrator, err := build(*repo, "", out)
	if err != nil {
		return err
	}
	ctx, stop := signalContext()
	defer stop()
	if err := orchestrator.Reconcile(ctx, *repo); err != nil {
		return err
	}
	fmt.Fprintln(out, "未確認のマージを処理しました。")
	return nil
}

func clearStop(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("clear-stop", flag.ContinueOnError)
	repo := flags.String("repo", "", "owner/name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *repo == "" {
		return errors.New("--repo は必須です")
	}
	root, err := stateRoot()
	if err != nil {
		return err
	}
	if err := (harness.Store{Root: root}).ClearStop(*repo); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s の停止を解除しました。\n", *repo)
	return nil
}

// claudeLoggedIn は無人のレビューに必要な CLI のログインを確認する。デスクトップアプリとは別に必要である。
func claudeLoggedIn(status string) error {
	var state struct {
		LoggedIn bool `json:"loggedIn"`
	}
	if err := json.Unmarshal([]byte(status), &state); err != nil {
		return fmt.Errorf("claude auth status を解釈できません: %w", err)
	}
	if !state.LoggedIn {
		return errors.New("ターミナルの claude がログインしていません")
	}
	return nil
}

// doctor は準備の状態を確認する。秘密情報の値は表示しない。
func doctor(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	repo := flags.String("repo", "", "owner/name")
	codexAuth := flags.String("codex-auth", "chatgpt", "Codex の認証: chatgpt, api-key")
	model := flags.String("model", defaultModel, "run で使うモデル")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *repo == "" {
		return errors.New("--repo は必須です")
	}
	failed := false
	check := func(name string, err error) {
		if err != nil {
			failed = true
			fmt.Fprintf(out, "FAIL %s: %v\n", name, err)
			return
		}
		fmt.Fprintf(out, "PASS %s\n", name)
	}
	for _, name := range []string{"codex", "claude", "op", "gh", "git", "mise"} {
		_, err := resolve(name)
		check(name, err)
	}
	root, rootErr := stateRoot()
	check("状態ディレクトリ", rootErr)
	if rootErr == nil {
		if err := os.MkdirAll(root, 0o700); err != nil {
			check("状態ディレクトリの作成", err)
		}
		mise, miseErr := resolve("mise")
		if miseErr == nil {
			miseErr = harness.VerifyScanner(context.Background(), scanner(harness.OSRunner{}, mise, root))
		}
		check("秘密情報スキャンの検出", miseErr)
	}
	check("Codex の権限プロファイル", installProfile())
	codexHome, err := codexHomeFor(*codexAuth)
	if err == nil && codexHome != "" {
		// 実行時と同じ実体で確認する。shim のままだと別の版で判定することがある。
		var codex string
		if codex, err = resolve("codex"); err == nil {
			err = harness.APIKeyLoggedIn(context.Background(), harness.OSRunner{}, codex, codexHome)
		}
	}
	authErr := err
	if *codexAuth != "chatgpt" {
		check("Codex の API キー", authErr)
		if codexHome != "" {
			home, homeErr := os.UserHomeDir()
			if homeErr == nil {
				homeErr = harness.APIKeyProfileCurrent(codexHome, harnessassets.CodexProfile, workRoot(home))
			}
			check("API キー用ホームの権限プロファイル", homeErr)
		}
	}
	// 認証できないと一覧を取れず、原因と違う失敗が並ぶので、認証の失敗だけを示す。
	if authErr == nil {
		check("Codex のモデル "+*model, checkModel(*codexAuth, *model))
	}
	// 実際のレビューと同じ実体で確認する。
	claude, err := resolve("claude")
	if err == nil {
		var status string
		status, err = harness.OSRunner{}.Run(context.Background(), harness.Cmd{Name: claude, Args: []string{"auth", "status"}})
		if err == nil {
			err = claudeLoggedIn(status)
		}
	}
	check("Claude のログイン", err)
	tokens := &harness.AppTokens{
		Secrets: harness.OPSecrets{Run: harness.OSRunner{}},
		Repo:    *repo,
		API:     harness.GitHubAPI,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
		Now:     time.Now,
	}
	_, err = tokens.Token(context.Background())
	check("GitHub App の短命トークン", err)
	if failed {
		return errors.New("準備が完了していません")
	}
	return nil
}
