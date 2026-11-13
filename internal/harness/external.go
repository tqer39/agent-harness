//go:build !windows

package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	maxCommentRunes = 1500
	// ExternalReviewGrace は外部レビューの check が現れるまでの猶予である。導入していないリポジトリを止めない。
	ExternalReviewGrace = 3 * time.Minute
)

var (
	htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	htmlDetails = regexp.MustCompile(`(?s)<details>.*?</details>`)
	htmlTag     = regexp.MustCompile(`<[^>]+>`)
	blankLines  = regexp.MustCompile(`\n{2,}`)
	// 誤検知は人間の確認に倒れるだけなので、広めに止める。
	securityText = regexp.MustCompile(`(?i)security|critical|脆弱性|セキュリティ`)
)

// ReviewThread は未解決のレビュースレッドである。Body は信頼できないデータとして扱う。
type ReviewThread struct {
	ID     string
	Author string
	Bot    bool
	Path   string
	Line   int
	Body   string
	// Sensitive は切り詰める前の本文にセキュリティの語があることを表す。
	Sensitive bool
	Replies   []ThreadComment
}

// ThreadComment はスレッドの 2 件目以降のコメントである。
type ThreadComment struct {
	Author    string
	Bot       bool
	Body      string
	Sensitive bool
}

// FedThread は実装担当に修正を依頼した外部レビューの指摘である。
type FedThread struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// visibleComment は表示されない部分を除く。隠れた文章を実装担当に渡さないためである。
func visibleComment(body string) string {
	body = htmlComment.ReplaceAllString(body, "")
	body = htmlDetails.ReplaceAllString(body, "")
	body = htmlTag.ReplaceAllString(body, "")
	return strings.TrimSpace(blankLines.ReplaceAllString(body, "\n"))
}

func cleanComment(body string) string {
	return truncate(visibleComment(body), maxCommentRunes)
}

const threadsQuery = `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){pullRequest(number:$number){reviewThreads(first:100){pageInfo{hasNextPage}nodes{id isResolved path line comments(first:100){pageInfo{hasNextPage}nodes{body author{__typename login}}}}}}}}`

// UnresolvedThreads は未解決のレビュースレッドを返す。
func (g *GitHub) UnresolvedThreads(ctx context.Context, pr int) ([]ReviewThread, error) {
	owner, name, _ := strings.Cut(g.Repo, "/")
	out, err := g.gh(ctx, "api", "graphql", "-f", "query="+threadsQuery, "-f", "owner="+owner, "-f", "name="+name, "-F", "number="+strconv.Itoa(pr))
	if err != nil {
		return nil, err
	}
	var data struct {
		Data struct {
			Repository struct {
				PullRequest struct {
					ReviewThreads struct {
						PageInfo struct {
							HasNextPage bool `json:"hasNextPage"`
						} `json:"pageInfo"`
						Nodes []struct {
							ID         string `json:"id"`
							IsResolved bool   `json:"isResolved"`
							Path       string `json:"path"`
							Line       int    `json:"line"`
							Comments   struct {
								PageInfo struct {
									HasNextPage bool `json:"hasNextPage"`
								} `json:"pageInfo"`
								Nodes []struct {
									Body   string `json:"body"`
									Author struct {
										Type  string `json:"__typename"`
										Login string `json:"login"`
									} `json:"author"`
								} `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &data); err != nil {
		return nil, fmt.Errorf("レビュースレッドを解釈できません: %w", err)
	}
	threads := data.Data.Repository.PullRequest.ReviewThreads
	if threads.PageInfo.HasNextPage {
		return nil, fmt.Errorf("PR #%d のレビュースレッドが多すぎます", pr)
	}
	var result []ReviewThread
	for _, node := range threads.Nodes {
		if node.IsResolved {
			continue
		}
		if node.Comments.PageInfo.HasNextPage {
			return nil, fmt.Errorf("PR #%d のスレッド %s のコメントが多すぎます", pr, node.Path)
		}
		thread := ReviewThread{ID: node.ID, Path: node.Path, Line: node.Line}
		for i, comment := range node.Comments.Nodes {
			item := ThreadComment{
				Author:    comment.Author.Login,
				Bot:       comment.Author.Type == "Bot",
				Body:      cleanComment(comment.Body),
				Sensitive: securityText.MatchString(visibleComment(comment.Body)),
			}
			if i == 0 {
				thread.Author, thread.Bot, thread.Body, thread.Sensitive = item.Author, item.Bot, item.Body, item.Sensitive
			} else {
				thread.Replies = append(thread.Replies, item)
			}
		}
		result = append(result, thread)
	}
	return result, nil
}

// ResolveThread はレビュースレッドを解決済みにする。
func (g *GitHub) ResolveThread(ctx context.Context, id string) error {
	_, err := g.gh(ctx, "api", "graphql", "-f", "query=mutation($id:ID!){resolveReviewThread(input:{threadId:$id}){thread{id}}}", "-f", "id="+id)
	return err
}

// WaitExternalReview は外部レビューの完了を待つ。猶予内に現れなければ未導入として進める。
func (g *GitHub) WaitExternalReview(ctx context.Context, sha, name string, grace time.Duration) error {
	deadline := time.Now().Add(grace)
	for {
		runs, err := g.checkRuns(ctx, sha, name)
		if err != nil {
			return err
		}
		if len(runs) > 0 {
			// 結果は問わない。指摘の有無はスレッドで判断する。
			if allCompleted(runs) {
				return nil
			}
		} else if !time.Now().Before(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s の完了を確認できません: %w", name, ctx.Err())
		case <-time.After(g.Poll):
		}
	}
}

type threadClass struct {
	New        []ReviewThread
	StopReason string
}

// classifyThreads はスレッドを修正依頼か人間の判断に分ける。
// 人間の発言と Bot を装った利用者の発言は、LLM に任せず人間に返す。
// 修正を依頼済みのスレッドでも、後からの返信や編集を見逃さないよう全コメントを検査する。
func classifyThreads(threads []ReviewThread, config RepoConfig, fed []FedThread) threadClass {
	var result threadClass
	for _, thread := range threads {
		comments := append([]ThreadComment{{Author: thread.Author, Bot: thread.Bot, Body: thread.Body, Sensitive: thread.Sensitive}}, thread.Replies...)
		for _, comment := range comments {
			if !comment.Bot || !slices.Contains(config.ExternalReviewAuthors, comment.Author) {
				result.StopReason = fmt.Sprintf("%s の未解決のコメントがあります: %s:%d", comment.Author, thread.Path, thread.Line)
				return result
			}
			if comment.Sensitive || securityText.MatchString(comment.Body) {
				result.StopReason = fmt.Sprintf("%s がセキュリティまたは重大な問題を指摘しました: %s:%d", comment.Author, thread.Path, thread.Line)
				return result
			}
		}
		// 依頼済みでも、Bot の返信や編集で内容が変われば改めて依頼する。
		if !slices.ContainsFunc(fed, func(item FedThread) bool { return item.ID == thread.ID && item.Text == formatThread(thread) }) {
			result.New = append(result.New, thread)
		}
	}
	return result
}

func formatThread(thread ReviewThread) string {
	text := fmt.Sprintf("%s:%d (%s)\n%s", thread.Path, thread.Line, thread.Author, thread.Body)
	for _, reply := range thread.Replies {
		text += fmt.Sprintf("\n返信 (%s): %s", reply.Author, reply.Body)
	}
	return text
}

// recordFedThread は依頼した内容を記録する。同じスレッドは最新の内容に置き換える。
func recordFedThread(fed []FedThread, thread ReviewThread) []FedThread {
	text := formatThread(thread)
	for i := range fed {
		if fed[i].ID == thread.ID {
			fed[i].Text = text
			return fed
		}
	}
	return append(fed, FedThread{ID: thread.ID, Text: text})
}
