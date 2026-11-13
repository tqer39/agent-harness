//go:build !windows

package harness

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// OpenAIKeyRef は専用 Vault にある OpenAI の API キーである。
const OpenAIKeyRef = "op://agent-harness/openai/api-key"

// 専用ホームのキーをファイルに保存する。保存先は実装担当の読み取り拒否の範囲にある。
const apiHomeConfig = "forced_login_method = \"api\"\ncli_auth_credentials_store = \"file\"\n"

// SetupAPIKey は API キー専用の Codex ホームを作る。
// ~/.codex の ChatGPT ログインと自律保守に影響しないよう、ホームを分ける。
// キーは標準入力でだけ渡し、引数と環境変数に出さない。
func SetupAPIKey(ctx context.Context, run Runner, secrets Secrets, codex, home string, profile []byte) error {
	key, err := secrets.Read(ctx, OpenAIKeyRef)
	if err != nil {
		return err
	}
	if strings.TrimSpace(key) == "" {
		return errors.New("1Password の OpenAI の API キーが空です")
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(home, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(apiHomeConfig), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(home, "agent-harness.config.toml"), profile, 0o600); err != nil {
		return err
	}
	if _, err := run.Run(ctx, Cmd{
		Name:    codex,
		Args:    []string{"login", "--with-api-key"},
		Env:     []string{"CODEX_HOME=" + home},
		Stdin:   key,
		Secrets: []string{key},
	}); err != nil {
		return fmt.Errorf("Codex に API キーを登録できません: %w", err)
	}
	return nil
}

// APIKeyLoggedIn は専用ホームが API キーでログインしていることを確認する。
func APIKeyLoggedIn(ctx context.Context, run Runner, codex, home string) error {
	out, err := run.Run(ctx, Cmd{Name: codex, Args: []string{"login", "status"}, Env: []string{"CODEX_HOME=" + home}, CombineOutput: true})
	var runErr *RunError
	// 未ログインの案内にも "API key" が含まれるので、終了コードとログイン済みの文で判定する。
	if errors.As(err, &runErr) || (err == nil && !strings.Contains(strings.ToLower(out), "logged in using an api key")) {
		return errors.New("API キーでログインしていません。agent-harness setup-api-key を実行してください")
	}
	return err
}

// APIKeyProfileCurrent は専用ホームの権限プロファイルが埋め込みの版と同じかを確認する。
// setup-api-key の時点の複製なので、更新後に再実行しないと古い制限のまま動く。
func APIKeyProfileCurrent(home string, profile []byte, workRoot string) error {
	current, err := os.ReadFile(filepath.Join(home, "agent-harness.config.toml"))
	if err != nil {
		return err
	}
	if !ProfileMatches(current, profile, workRoot) {
		return errors.New("API キー用ホームの権限プロファイルが古いです。agent-harness setup-api-key を再実行してください")
	}
	return nil
}
