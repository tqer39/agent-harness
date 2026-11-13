//go:build !windows

package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Result は実装担当の完了の申告である。信用せず、差分と検査で確認する。
type Result struct {
	RunID   string `json:"run_id"`
	Round   int    `json:"round"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
}

// ResultSchema は実装担当の最後の応答の形である。codex exec の --output-schema に渡す。
const ResultSchema = `{"type":"object","additionalProperties":false,"required":["run_id","round","status","summary"],"properties":{"run_id":{"type":"string"},"round":{"type":"integer"},"status":{"type":"string","enum":["done","blocked"]},"summary":{"type":"string"}}}`

func ReadResult(workdir, runID string, round int) (Result, bool, error) {
	data, err := os.ReadFile(filepath.Join(workdir, ".agent-run", "result.json"))
	if errors.Is(err, os.ErrNotExist) {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, err
	}
	var result Result
	if err := json.Unmarshal(data, &result); err != nil {
		// 書き込み途中の可能性があるので、次の確認まで待つ。
		return Result{}, false, nil
	}
	if result.RunID != runID || result.Round != round {
		return Result{}, false, nil
	}
	return result, true, nil
}

type Issue struct {
	Summary string `json:"summary"`
	Path    string `json:"path,omitempty"`
}

type Criterion struct {
	Criterion string `json:"criterion"`
	Met       bool   `json:"met"`
}

type Verdict struct {
	SHA              string      `json:"sha"`
	Approved         bool        `json:"approved"`
	SecurityFindings []Issue     `json:"security_findings"`
	Findings         []Issue     `json:"findings"`
	Criteria         []Criterion `json:"criteria"`
	Reason           string      `json:"reason"`
}

type Decision int

const (
	DecisionPass Decision = iota
	DecisionRepair
	DecisionStop
)

// Decide はレビューの出力を決定的に解釈する。セキュリティの指摘と判定の不備は止める。
func (v Verdict) Decide() Decision {
	if len(v.SecurityFindings) > 0 || len(v.Criteria) == 0 {
		return DecisionStop
	}
	if !v.Approved || len(v.Findings) > 0 {
		return DecisionRepair
	}
	for _, criterion := range v.Criteria {
		if !criterion.Met {
			return DecisionRepair
		}
	}
	return DecisionPass
}

var listMarker = regexp.MustCompile(`^(?:[-*+]\s+(?:\[[ xX]\]\s+)?|\d+[.)]\s+)`)

func normalizeCriterion(line string) string {
	line = strings.TrimSpace(line)
	return strings.TrimSpace(listMarker.ReplaceAllString(line, ""))
}

// CriteriaItems は受入条件を 1 行 1 条件として取り出す。
func CriteriaItems(text string) []string {
	var items []string
	for _, line := range strings.Split(text, "\n") {
		if item := normalizeCriterion(line); item != "" {
			items = append(items, item)
		}
	}
	return items
}

// MissingCriteria はレビューで判定されなかった受入条件を返す。省略による合格を防ぐ。
func (v Verdict) MissingCriteria(text string) []string {
	judged := map[string]bool{}
	for _, criterion := range v.Criteria {
		judged[normalizeCriterion(criterion.Criterion)] = true
	}
	var missing []string
	for _, item := range CriteriaItems(text) {
		if !judged[item] {
			missing = append(missing, item)
		}
	}
	return missing
}

func ParseVerdict(data []byte, wantSHA string) (Verdict, error) {
	var envelope struct {
		Structured json.RawMessage `json:"structured_output"`
	}
	body := data
	if err := json.Unmarshal(data, &envelope); err == nil && len(envelope.Structured) > 0 {
		body = envelope.Structured
	}
	var verdict Verdict
	if err := json.Unmarshal(body, &verdict); err != nil {
		return Verdict{}, fmt.Errorf("レビューの判定を解釈できません: %w", err)
	}
	if verdict.SHA != wantSHA {
		return Verdict{}, fmt.Errorf("レビューの SHA が一致しません: %q", verdict.SHA)
	}
	return verdict, nil
}

// VerdictSchema は claude --json-schema に渡すレビュー出力の形式である。
const VerdictSchema = `{"type":"object","additionalProperties":false,"required":["sha","approved","security_findings","findings","criteria","reason"],"properties":{"sha":{"type":"string"},"approved":{"type":"boolean"},"security_findings":{"type":"array","items":{"$ref":"#/$defs/issue"}},"findings":{"type":"array","items":{"$ref":"#/$defs/issue"}},"criteria":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["criterion","met"],"properties":{"criterion":{"type":"string"},"met":{"type":"boolean"}}}},"reason":{"type":"string"}},"$defs":{"issue":{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string"},"path":{"type":"string"}}}}}`
