package oauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuthURLContainsExpectedParams(t *testing.T) {
	m, err := NewLoginManager("https://auth.openai.com", "client", "http://localhost:1455/auth/callback", &http.Client{Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("NewLoginManager: %v", err)
	}

	u, err := url.Parse(m.AuthURL())
	if err != nil {
		t.Fatalf("parse auth url: %v", err)
	}
	q := u.Query()
	if q.Get("client_id") != "client" {
		t.Fatalf("client_id=%q", q.Get("client_id"))
	}
	if q.Get("redirect_uri") != "http://localhost:1455/auth/callback" {
		t.Fatalf("redirect_uri=%q", q.Get("redirect_uri"))
	}
	if q.Get("scope") != "openid profile email offline_access" {
		t.Fatalf("scope=%q", q.Get("scope"))
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Fatalf("method=%q", q.Get("code_challenge_method"))
	}
	if q.Get("codex_cli_simplified_flow") != "true" {
		t.Fatalf("codex_cli_simplified_flow=%q", q.Get("codex_cli_simplified_flow"))
	}
	if q.Get("state") == "" {
		t.Fatal("state is empty")
	}
	if q.Get("code_challenge") == "" {
		t.Fatal("code_challenge is empty")
	}
}

func TestExchangeCodeAndTokenExchange(t *testing.T) {
	idToken := jwtForClaims(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acc-1"},
		"organization_id":             "org-1",
		"project_id":                  "proj-1",
	})
	accessToken := jwtForClaims(map[string]any{"chatgpt_plan_type": "plus"})

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Method != http.MethodPost {
			t.Fatalf("method=%s", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
			t.Fatalf("content-type=%q", got)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}

		switch r.Form.Get("grant_type") {
		case "authorization_code":
			if r.Form.Get("code") != "auth-code" {
				t.Fatalf("code=%q", r.Form.Get("code"))
			}
			writeJSON(w, map[string]any{
				"id_token":      idToken,
				"access_token":  accessToken,
				"refresh_token": "refresh-1",
			})
		case "urn:ietf:params:oauth:grant-type:token-exchange":
			if r.Form.Get("requested_token") != "openai-api-key" {
				t.Fatalf("requested_token=%q", r.Form.Get("requested_token"))
			}
			if r.Form.Get("subject_token") != idToken {
				t.Fatalf("subject_token mismatch")
			}
			writeJSON(w, map[string]any{"access_token": "sk-test"})
		default:
			t.Fatalf("unexpected grant_type=%q", r.Form.Get("grant_type"))
		}
	}))
	defer srv.Close()

	m, err := NewLoginManager(srv.URL, "client", "http://localhost:1455/auth/callback", srv.Client())
	if err != nil {
		t.Fatalf("NewLoginManager: %v", err)
	}
	m.now = func() time.Time { return time.Date(2026, 2, 22, 12, 0, 0, 0, time.UTC) }

	auth, err := m.ExchangeCode(context.Background(), "auth-code")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if hits != 2 {
		t.Fatalf("hits=%d", hits)
	}
	if auth.Tokens.AccountID != "acc-1" {
		t.Fatalf("account_id=%q", auth.Tokens.AccountID)
	}
	if auth.OpenAIAPIKey == nil || *auth.OpenAIAPIKey != "sk-test" {
		t.Fatalf("api key=%v", auth.OpenAIAPIKey)
	}
}

func TestExchangeCodeWithoutOrgProjectSkipsTokenExchange(t *testing.T) {
	idToken := jwtForClaims(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acc-1"},
	})
	accessToken := jwtForClaims(map[string]any{"chatgpt_plan_type": "plus"})

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "authorization_code" {
			t.Fatalf("grant_type=%q", r.Form.Get("grant_type"))
		}
		writeJSON(w, map[string]any{
			"id_token":      idToken,
			"access_token":  accessToken,
			"refresh_token": "refresh-1",
		})
	}))
	defer srv.Close()

	m, err := NewLoginManager(srv.URL, "client", "http://localhost:1455/auth/callback", srv.Client())
	if err != nil {
		t.Fatalf("NewLoginManager: %v", err)
	}
	auth, err := m.ExchangeCode(context.Background(), "auth-code")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if hits != 1 {
		t.Fatalf("hits=%d", hits)
	}
	if auth.OpenAIAPIKey != nil {
		t.Fatalf("unexpected api key: %v", *auth.OpenAIAPIKey)
	}
}

func TestWriteAuthFile(t *testing.T) {
	dir := t.TempDir()
	key := "sk-test"
	auth := &AuthFile{
		OpenAIAPIKey: &key,
		Tokens: TokenData{
			IDToken:      "id",
			AccessToken:  "acc",
			RefreshToken: "ref",
			AccountID:    "account",
		},
		LastRefresh: time.Date(2026, 2, 22, 12, 0, 0, 0, time.UTC),
	}
	if err := writeAuthFile(dir, auth); err != nil {
		t.Fatalf("writeAuthFile: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dir, "auth.json"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(b), "\"OPENAI_API_KEY\": \"sk-test\"") {
		t.Fatalf("auth.json missing key: %s", string(b))
	}
}

func TestWriteAuthFilePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom", "tokens.json")
	auth := &AuthFile{
		Tokens: TokenData{
			AccessToken: "acc",
			AccountID:   "account",
		},
		LastRefresh: time.Date(2026, 2, 22, 12, 0, 0, 0, time.UTC),
	}
	if err := writeAuthFilePath(path, auth); err != nil {
		t.Fatalf("writeAuthFilePath: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(b), "\"access_token\": \"acc\"") {
		t.Fatalf("custom auth file missing access token: %s", string(b))
	}
}

func jwtForClaims(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(b)
	return "x." + payload + ".y"
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
