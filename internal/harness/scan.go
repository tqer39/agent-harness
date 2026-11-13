//go:build !windows

package harness

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
)

// betterleaksLeakExit は検出専用の終了コードである。
// 既定の 1 は起動エラーと区別できないことがあるので、検出に使わない。
const betterleaksLeakExit = 42

// Betterleaks は差分を検査する。PR 側の設定で検査を緩められないよう、Dir で実行する。
type Betterleaks struct {
	Run Runner
	Bin string
	// Prefix は Bin に続く引数である。mise で版を固定して起動するときに使う。
	Prefix []string
	Dir    string
}

func (g Betterleaks) Scan(ctx context.Context, patch []byte) (bool, error) {
	args := append(append([]string{}, g.Prefix...), "stdin", "--no-banner", "--redact", "--exit-code", strconv.Itoa(betterleaksLeakExit))
	_, err := g.Run.Run(ctx, Cmd{Dir: g.Dir, Name: g.Bin, Args: args, Stdin: string(patch), Env: []string{"MISE_YES=1"}})
	if err == nil {
		return false, nil
	}
	var runErr *RunError
	if errors.As(err, &runErr) && runErr.Code == betterleaksLeakExit {
		return true, nil
	}
	return false, fmt.Errorf("秘密情報スキャンを実行できません: %w", err)
}

// VerifyScanner はスキャナーが実際に検出と通過の両方をできることを確認する。
// 検査用の値はその場で生成する架空の値で、どこにも送らない。
func VerifyScanner(ctx context.Context, scanner Scanner) error {
	leak, err := scanner.Scan(ctx, []byte("+hello\n"))
	if err != nil {
		return err
	}
	if leak {
		return errors.New("問題のない差分を検出しました")
	}
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	random := make([]byte, 36)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	for i := range random {
		random[i] = letters[int(random[i])%len(letters)]
	}
	leak, err = scanner.Scan(ctx, []byte("+token = \"ghp_"+string(random)+"\"\n"))
	if err != nil {
		return err
	}
	if !leak {
		return errors.New("架空のトークンを検出できません")
	}
	return nil
}
