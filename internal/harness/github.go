//go:build !windows

package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GitHub は短命トークンで PR を操作する。トークンは環境変数でだけ渡す。
type GitHub struct {
	Run    Runner
	Tokens TokenSource
	Repo   string
	Poll   time.Duration
}

func (g *GitHub) gh(ctx context.Context, args ...string) (string, error) {
	token, err := g.Tokens.Token(ctx)
	if err != nil {
		return "", err
	}
	return g.Run.Run(ctx, Cmd{Name: "gh", Args: args, Env: []string{"GH_TOKEN=" + token}, Secrets: []string{token}})
}

// OpenPullRequest はブランチの未マージの PR を返す。なければ 0 を返す。
func (g *GitHub) OpenPullRequest(ctx context.Context, head string) (int, error) {
	out, err := g.gh(ctx, "pr", "list", "--repo", g.Repo, "--head", head, "--state", "open", "--json", "number")
	if err != nil {
		return 0, err
	}
	var prs []struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal([]byte(out), &prs); err != nil {
		return 0, err
	}
	if len(prs) == 0 {
		return 0, nil
	}
	return prs[0].Number, nil
}

func (g *GitHub) CreatePR(ctx context.Context, head, base, title, body string) (int, error) {
	out, err := g.gh(ctx, "pr", "create", "--repo", g.Repo, "--head", head, "--base", base, "--title", title, "--body", body)
	if err != nil {
		return 0, err
	}
	parts := strings.Split(strings.TrimSpace(out), "/")
	number, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return 0, fmt.Errorf("PR 番号を解釈できません: %q", out)
	}
	return number, nil
}

func (g *GitHub) SetStatus(ctx context.Context, sha, state, description string) error {
	_, err := g.gh(ctx, "api", "-X", "POST", "repos/"+g.Repo+"/statuses/"+sha,
		"-f", "state="+state, "-f", "context=agent-review", "-f", "description="+truncate(description, 140))
	return err
}

// WaitCheck は指定したチェックの完了を待ち、成功かどうかを返す。
func (g *GitHub) WaitCheck(ctx context.Context, sha, name string) (bool, error) {
	for {
		runs, err := g.checkRuns(ctx, sha, name)
		if err != nil {
			return false, err
		}
		if len(runs) > 0 && allCompleted(runs) {
			for _, run := range runs {
				if run.Conclusion != "success" {
					return false, nil
				}
			}
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, fmt.Errorf("%s の完了を確認できません: %w", name, ctx.Err())
		case <-time.After(g.Poll):
		}
	}
}

type checkRun struct {
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

// checkRuns は同じ名前のチェックをすべて返す。push と pull_request の両方で起動すると複数になる。
func (g *GitHub) checkRuns(ctx context.Context, sha, name string) ([]checkRun, error) {
	out, err := g.gh(ctx, "api", "repos/"+g.Repo+"/commits/"+sha+"/check-runs?check_name="+url.QueryEscape(name)+"&filter=latest")
	if err != nil {
		return nil, err
	}
	var data struct {
		Runs []checkRun `json:"check_runs"`
	}
	if err := json.Unmarshal([]byte(out), &data); err != nil {
		return nil, err
	}
	return data.Runs, nil
}

func allCompleted(runs []checkRun) bool {
	for _, run := range runs {
		if run.Status != "completed" {
			return false
		}
	}
	return true
}

// FailedLog は修正の材料になる CI のログを返す。内容は信頼できないデータとして扱う。
func (g *GitHub) FailedLog(ctx context.Context, sha string) (string, error) {
	out, err := g.gh(ctx, "run", "list", "--repo", g.Repo, "--commit", sha, "--status", "failure", "--json", "databaseId", "--jq", ".[0].databaseId")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(out)
	if id == "" {
		return "", nil
	}
	log, err := g.gh(ctx, "run", "view", id, "--repo", g.Repo, "--log-failed")
	return tail(log, 8000), err
}

func (g *GitHub) ArmMerge(ctx context.Context, pr int, sha string) error {
	_, err := g.gh(ctx, "pr", "merge", strconv.Itoa(pr), "--repo", g.Repo, "--auto", "--squash", "--match-head-commit", sha)
	return err
}

// WaitMerged はマージを待つ。待機中に先頭が変わったら止める。
func (g *GitHub) WaitMerged(ctx context.Context, pr int, sha string) (string, error) {
	for {
		out, err := g.gh(ctx, "pr", "view", strconv.Itoa(pr), "--repo", g.Repo, "--json", "state,headRefOid,mergeCommit")
		if err != nil {
			return "", err
		}
		var state struct {
			State       string `json:"state"`
			Head        string `json:"headRefOid"`
			MergeCommit *struct {
				OID string `json:"oid"`
			} `json:"mergeCommit"`
		}
		if err := json.Unmarshal([]byte(out), &state); err != nil {
			return "", err
		}
		if state.Head != sha {
			return "", fmt.Errorf("PR #%d の先頭が変わりました", pr)
		}
		switch state.State {
		case "MERGED":
			if state.MergeCommit == nil {
				return "", fmt.Errorf("PR #%d のマージコミットを確認できません", pr)
			}
			return state.MergeCommit.OID, nil
		case "CLOSED":
			return "", fmt.Errorf("PR #%d が閉じられました", pr)
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("PR #%d のマージを確認できません: %w", pr, ctx.Err())
		case <-time.After(g.Poll):
		}
	}
}

// PullRequestMerge は PR の状態とマージコミットを返す。待機後のマージの追跡に使う。
func (g *GitHub) PullRequestMerge(ctx context.Context, pr int) (PullRequestState, error) {
	out, err := g.gh(ctx, "pr", "view", strconv.Itoa(pr), "--repo", g.Repo, "--json", "state,mergeCommit,autoMergeRequest,labels")
	if err != nil {
		return PullRequestState{}, err
	}
	var view struct {
		State       string `json:"state"`
		MergeCommit *struct {
			OID string `json:"oid"`
		} `json:"mergeCommit"`
		AutoMergeRequest *struct{} `json:"autoMergeRequest"`
		Labels           []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		return PullRequestState{}, err
	}
	state := PullRequestState{State: view.State, AutoMerge: view.AutoMergeRequest != nil}
	if view.MergeCommit != nil {
		state.MergeSHA = view.MergeCommit.OID
	}
	for _, label := range view.Labels {
		state.NeedsHuman = state.NeedsHuman || label.Name == "needs-human"
	}
	return state, nil
}

func (g *GitHub) MarkNeedsHuman(ctx context.Context, pr int, reason string) error {
	if _, err := g.gh(ctx, "label", "create", "needs-human", "--repo", g.Repo, "--force", "--description", "ハーネスが人間の判断を待っています"); err != nil {
		return err
	}
	if _, err := g.gh(ctx, "pr", "edit", strconv.Itoa(pr), "--repo", g.Repo, "--add-label", "needs-human"); err != nil {
		return err
	}
	_, err := g.gh(ctx, "pr", "comment", strconv.Itoa(pr), "--repo", g.Repo, "--body", "agent-harness が停止しました。\n\n"+reason)
	return err
}

func (g *GitHub) Comment(ctx context.Context, pr int, body string) error {
	_, err := g.gh(ctx, "pr", "comment", strconv.Itoa(pr), "--repo", g.Repo, "--body", body)
	return err
}

func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}
