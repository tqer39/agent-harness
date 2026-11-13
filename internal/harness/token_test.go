//go:build !windows

package harness

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeSecrets map[string]string

func (f fakeSecrets) Read(_ context.Context, ref string) (string, error) { return f[ref], nil }

func testKeyPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

func TestAppTokensRestrictsRepositoryAndCaches(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Errorf("missing JWT")
		}
		switch r.URL.Path {
		case "/repos/o/n/installation":
			io.WriteString(w, `{"id":42,"permissions":{"contents":"write","pull_requests":"write","statuses":"write","metadata":"read","checks":"read","actions":"read"}}`)
		case "/app/installations/42/access_tokens":
			calls++
			var body struct {
				Repositories []string          `json:"repositories"`
				Permissions  map[string]string `json:"permissions"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if len(body.Repositories) != 1 || body.Repositories[0] != "n" {
				t.Errorf("repositories=%v", body.Repositories)
			}
			if _, ok := body.Permissions["workflows"]; ok {
				t.Errorf("workflows permission must not be requested")
			}
			io.WriteString(w, `{"token":"short-lived"}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	tokens := &AppTokens{
		Secrets: fakeSecrets{AppIDRef: "123", AppKeyRef: testKeyPEM(t)},
		Repo:    "o/n",
		API:     server.URL,
		HTTP:    server.Client(),
		Now:     func() time.Time { return time.Unix(1_800_000_000, 0) },
	}
	for range 2 {
		token, err := tokens.Token(context.Background())
		if err != nil || token != "short-lived" {
			t.Fatalf("token=%q err=%v", token, err)
		}
	}
	if calls != 1 {
		t.Fatalf("token was minted %d times", calls)
	}
}

func TestOPSecretsPassesTokenOnlyToOP(t *testing.T) {
	runner := &scriptRunner{responses: map[string]response{
		"security": {out: "sa-token\n"},
		"op":       {out: "value\n"},
	}}
	value, err := OPSecrets{Run: runner}.Read(context.Background(), AppIDRef)
	if err != nil || value != "value" {
		t.Fatalf("value=%q err=%v", value, err)
	}
	if runner.calls[0].Name != "security" || len(runner.calls[0].Env) != 0 {
		t.Fatalf("call=%+v", runner.calls[0])
	}
	op := runner.calls[1]
	if op.Name != "op" || op.Env[0] != "OP_SERVICE_ACCOUNT_TOKEN=sa-token" || op.Secrets[0] != "sa-token" {
		t.Fatalf("call=%+v", op)
	}
}

func TestParseAppKeyAcceptsCommonForms(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pkcs1 := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8 := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	cases := map[string]string{
		"PKCS1":        pkcs1,
		"PKCS8":        pkcs8,
		"改行が空白":        strings.ReplaceAll(strings.TrimSpace(pkcs1), "\n", " "),
		"改行が文字列":       strings.ReplaceAll(pkcs1, "\n", "\\n"),
		"CRLF":         strings.ReplaceAll(pkcs1, "\n", "\r\n"),
		"PKCS8 の改行が空白": strings.ReplaceAll(strings.TrimSpace(pkcs8), "\n", " "),
	}
	for name, text := range cases {
		got, err := parseAppKey(text)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !got.Equal(key) {
			t.Errorf("%s: different key", name)
		}
	}
}

func TestParseAppKeyErrorsDoNotContainKeyMaterial(t *testing.T) {
	const secret = "not-a-real-key-material"
	for _, text := range []string{"", secret, "-----BEGIN RSA PRIVATE KEY----- " + secret + " -----END RSA PRIVATE KEY-----"} {
		_, err := parseAppKey(text)
		if err == nil {
			t.Fatalf("%q was accepted", text)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("key material leaked: %v", err)
		}
	}
}

func TestAppTokensReportsMissingPermissionsWithoutMinting(t *testing.T) {
	minted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/o/n/installation" {
			io.WriteString(w, `{"id":42,"permissions":{"contents":"write","pull_requests":"read","metadata":"read","statuses":"write","actions":"read"}}`)
			return
		}
		minted = true
	}))
	defer server.Close()
	tokens := &AppTokens{
		Secrets: fakeSecrets{AppIDRef: "123", AppKeyRef: testKeyPEM(t)},
		Repo:    "o/n", API: server.URL, HTTP: server.Client(),
		Now: func() time.Time { return time.Unix(1_800_000_000, 0) },
	}
	_, err := tokens.Token(context.Background())
	if err == nil || minted {
		t.Fatalf("err=%v minted=%v", err, minted)
	}
	for _, want := range []string{"checks: read", "pull_requests: write"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q missing in %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "contents") {
		t.Errorf("granted permission was reported: %v", err)
	}
}

func TestAppTokensIncludesGitHubMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"message":"Integration not found"}`)
	}))
	defer server.Close()
	tokens := &AppTokens{
		Secrets: fakeSecrets{AppIDRef: "123", AppKeyRef: testKeyPEM(t)},
		Repo:    "o/n", API: server.URL, HTTP: server.Client(),
		Now: func() time.Time { return time.Unix(1_800_000_000, 0) },
	}
	if _, err := tokens.Token(context.Background()); err == nil || !strings.Contains(err.Error(), "Integration not found") {
		t.Fatalf("err=%v", err)
	}
}

// slowSecrets は読み取りのたびに時計を進め、op read が長く止まった状態を再現する。
type slowSecrets struct {
	values map[string]string
	clock  *time.Time
}

func (s slowSecrets) Read(_ context.Context, ref string) (string, error) {
	*s.clock = s.clock.Add(20 * time.Minute)
	return s.values[ref], nil
}

func TestAppTokensSignsJWTWithTimeAfterReadingSecrets(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	var issued int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
		data, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			t.Fatal(err)
		}
		var claims struct {
			IAT int64 `json:"iat"`
		}
		if err := json.Unmarshal(data, &claims); err != nil {
			t.Fatal(err)
		}
		issued = claims.IAT
		switch r.URL.Path {
		case "/repos/o/n/installation":
			io.WriteString(w, `{"id":42,"permissions":{"contents":"write","pull_requests":"write","statuses":"write","metadata":"read","checks":"read","actions":"read"}}`)
		default:
			io.WriteString(w, `{"token":"short-lived"}`)
		}
	}))
	defer server.Close()
	tokens := &AppTokens{
		Secrets: slowSecrets{values: map[string]string{AppIDRef: "123", AppKeyRef: testKeyPEM(t)}, clock: &clock},
		Repo:    "o/n",
		API:     server.URL,
		HTTP:    server.Client(),
		Now:     func() time.Time { return clock },
	}
	if _, err := tokens.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 2 回の読み取りで 40 分進んだ後の時刻で署名する。読み取り前の時刻だと exp が過去になる。
	if want := clock.Add(-time.Minute).Unix(); issued != want {
		t.Fatalf("iat=%d want %d", issued, want)
	}
}
