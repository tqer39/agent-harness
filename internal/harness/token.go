//go:build !windows

package harness

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	KeychainService = "agent-harness-op-token"
	AppIDRef        = "op://agent-harness/github-app/app-id"
	AppKeyRef       = "op://agent-harness/github-app/private-key"
	GitHubAPI       = "https://api.github.com"
)

type Secrets interface {
	Read(context.Context, string) (string, error)
}

// OPSecrets はキーチェーンのサービスアカウントのトークンで 1Password を読む。
// トークンは op の子プロセスにだけ渡し、生体認証を求めない。
type OPSecrets struct{ Run Runner }

func (s OPSecrets) Read(ctx context.Context, ref string) (string, error) {
	token, err := s.Run.Run(ctx, Cmd{Name: "security", Args: []string{"find-generic-password", "-s", KeychainService, "-w"}})
	if err != nil {
		return "", errors.New("キーチェーンからサービスアカウントのトークンを読めません")
	}
	token = strings.TrimSpace(token)
	value, err := s.Run.Run(ctx, Cmd{
		Name:    "op",
		Args:    []string{"read", ref},
		Env:     []string{"OP_SERVICE_ACCOUNT_TOKEN=" + token},
		Secrets: []string{token},
	})
	if err != nil {
		return "", fmt.Errorf("1Password から %s を読めません: %w", ref, err)
	}
	return strings.TrimSpace(value), nil
}

// requiredPermissions はハーネスが要求する権限である。workflows と administration は含めない。
var requiredPermissions = map[string]string{
	"contents": "write", "pull_requests": "write", "statuses": "write",
	"metadata": "read", "checks": "read", "actions": "read",
}

func missingPermissions(granted map[string]string) []string {
	var missing []string
	for name, level := range requiredPermissions {
		have := granted[name]
		if have == "write" || (have == "read" && level == "read") {
			continue
		}
		missing = append(missing, name+": "+level)
	}
	slices.Sort(missing)
	return missing
}

type TokenSource interface {
	Token(context.Context) (string, error)
}

// AppTokens は対象リポジトリに限定した短命トークンを発行する。
type AppTokens struct {
	Secrets Secrets
	Repo    string
	API     string
	HTTP    *http.Client
	Now     func() time.Time

	token   string
	expires time.Time
}

func (a *AppTokens) Token(ctx context.Context) (string, error) {
	now := a.Now()
	if a.token != "" && now.Before(a.expires) {
		return a.token, nil
	}
	appID, err := a.Secrets.Read(ctx, AppIDRef)
	if err != nil {
		return "", err
	}
	keyPEM, err := a.Secrets.Read(ctx, AppKeyRef)
	if err != nil {
		return "", err
	}
	// op read が長く止まることがあるので、署名の直前の時刻で iat と exp を決める。
	now = a.Now()
	jwt, err := signJWT(appID, keyPEM, now)
	if err != nil {
		return "", err
	}
	var installation struct {
		ID          int64             `json:"id"`
		Permissions map[string]string `json:"permissions"`
	}
	if err := a.call(ctx, http.MethodGet, "/repos/"+a.Repo+"/installation", jwt, nil, &installation); err != nil {
		return "", err
	}
	if missing := missingPermissions(installation.Permissions); len(missing) > 0 {
		return "", fmt.Errorf("GitHub App の権限が不足しています（設定後にインストール先で権限の更新を承認してください）: %s", strings.Join(missing, ", "))
	}
	_, name, _ := strings.Cut(a.Repo, "/")
	request := map[string]any{
		"repositories": []string{name},
		"permissions":  requiredPermissions,
	}
	var minted struct {
		Token string `json:"token"`
	}
	path := fmt.Sprintf("/app/installations/%d/access_tokens", installation.ID)
	if err := a.call(ctx, http.MethodPost, path, jwt, request, &minted); err != nil {
		return "", err
	}
	if minted.Token == "" {
		return "", errors.New("GitHub がトークンを返しませんでした")
	}
	a.token = minted.Token
	// 有効期限は 1 時間なので、余裕を持って更新する。
	a.expires = now.Add(50 * time.Minute)
	return a.token, nil
}

func (a *AppTokens) call(ctx context.Context, method, path, jwt string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, a.API+path, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+jwt)
	request.Header.Set("Accept", "application/vnd.github+json")
	response, err := a.HTTP.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		var detail struct {
			Message string `json:"message"`
		}
		// 本文に秘密情報は含まれない。読めなければ状態コードだけを返す。
		_ = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&detail)
		return fmt.Errorf("GitHub API %s が %d を返しました: %s", path, response.StatusCode, truncate(detail.Message, 200))
	}
	return json.NewDecoder(response.Body).Decode(out)
}

var pemBlock = regexp.MustCompile(`(?s)(-----BEGIN [A-Z ]+-----)(.*?)(-----END [A-Z ]+-----)`)

// parseAppKey は GitHub App の秘密鍵を読む。
// パスワード管理ツールで改行が空白や文字列の \\n に変わった場合も復元する。
// エラーに鍵の内容を含めない。
func parseAppKey(text string) (*rsa.PrivateKey, error) {
	text = strings.ReplaceAll(text, `\n`, "\n")
	match := pemBlock.FindStringSubmatch(text)
	if match == nil {
		return nil, errors.New("GitHub App の秘密鍵に PEM の開始行と終了行がありません")
	}
	body := strings.Join(strings.Fields(match[2]), "\n")
	block, _ := pem.Decode([]byte(match[1] + "\n" + body + "\n" + match[3] + "\n"))
	if block == nil {
		return nil, errors.New("GitHub App の秘密鍵の PEM を解釈できません")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("GitHub App の秘密鍵の形式を解釈できません")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("GitHub App の秘密鍵が RSA ではありません")
	}
	return key, nil
}

func signJWT(appID, keyPEM string, now time.Time) (string, error) {
	key, err := parseAppKey(keyPEM)
	if err != nil {
		return "", err
	}
	encode := base64.RawURLEncoding.EncodeToString
	header := encode([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{
		"iat": now.Add(-time.Minute).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": appID,
	})
	if err != nil {
		return "", err
	}
	signing := header + "." + encode(claims)
	digest := sha256.Sum256([]byte(signing))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signing + "." + encode(signature), nil
}
