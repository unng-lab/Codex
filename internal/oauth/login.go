package oauth

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"brainhub/internal/authstore"
)

const (
	DefaultClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	DefaultIssuer   = "https://auth.openai.com"
	RequiredPort    = 1455
)

var loginSuccessHTML = `<!DOCTYPE html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <title>Login successful</title>
  </head>
  <body>
    <div style="max-width: 640px; margin: 80px auto; font-family: system-ui, -apple-system, Segoe UI, Roboto, Helvetica, Arial, sans-serif;">
      <h1>Login successful</h1>
      <p>You can now close this window and return to the terminal.</p>
    </div>
  </body>
</html>
`

type TokenData struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id"`
}

type AuthFile struct {
	OpenAIAPIKey *string   `json:"OPENAI_API_KEY"`
	Tokens       TokenData `json:"tokens"`
	LastRefresh  time.Time `json:"last_refresh"`
}

type LoginOptions struct {
	NoBrowser bool
	Verbose   bool

	BindHost string
	Port     int

	ClientID string
	Issuer   string
	HomeDir  string
	AuthFile string

	Stdin  io.Reader
	Stderr io.Writer
}

type LoginManager struct {
	clientID    string
	issuer      string
	tokenURL    string
	redirectURI string
	state       string
	pkce        pkceCodes
	httpClient  *http.Client
	now         func() time.Time
	verbose     bool
	logf        func(format string, args ...any)
}

type pkceCodes struct {
	codeVerifier  string
	codeChallenge string
}

type authBundle struct {
	apiKey *string
	tokens TokenData
}

func NewLoginManager(issuer, clientID, redirectURI string, httpClient *http.Client) (*LoginManager, error) {
	if strings.TrimSpace(issuer) == "" {
		issuer = DefaultIssuer
	}
	if strings.TrimSpace(clientID) == "" {
		clientID = DefaultClientID
	}
	if strings.TrimSpace(redirectURI) == "" {
		return nil, fmt.Errorf("redirectURI is required")
	}
	pkce, err := generatePKCE()
	if err != nil {
		return nil, err
	}
	state, err := tokenHex(32)
	if err != nil {
		return nil, err
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	return &LoginManager{
		clientID:    clientID,
		issuer:      issuer,
		tokenURL:    issuer + "/oauth/token",
		redirectURI: redirectURI,
		state:       state,
		pkce:        pkce,
		httpClient:  httpClient,
		now:         time.Now,
		logf:        func(string, ...any) {},
	}, nil
}

func (m *LoginManager) AuthURL() string {
	v := url.Values{}
	v.Set("response_type", "code")
	v.Set("client_id", m.clientID)
	v.Set("redirect_uri", m.redirectURI)
	v.Set("scope", "openid profile email offline_access")
	v.Set("code_challenge", m.pkce.codeChallenge)
	v.Set("code_challenge_method", "S256")
	v.Set("id_token_add_organizations", "true")
	v.Set("codex_cli_simplified_flow", "true")
	v.Set("state", m.state)
	return m.issuer + "/oauth/authorize?" + v.Encode()
}

func (m *LoginManager) State() string { return m.state }

func (m *LoginManager) ExchangeCode(ctx context.Context, code string) (*AuthFile, error) {
	if strings.TrimSpace(code) == "" {
		return nil, fmt.Errorf("code is required")
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", m.redirectURI)
	form.Set("client_id", m.clientID)
	form.Set("code_verifier", m.pkce.codeVerifier)
	m.logf("oauth: exchanging authorization code at %s", m.tokenURL)

	tokensResp, err := m.postForm(ctx, form)
	if err != nil {
		return nil, err
	}

	idToken := stringValue(tokensResp["id_token"])
	accessToken := stringValue(tokensResp["access_token"])
	refreshToken := stringValue(tokensResp["refresh_token"])

	idClaims := parseJWTClaims(idToken)
	accessClaims := parseJWTClaims(accessToken)
	accountID := nestedString(idClaims, "https://api.openai.com/auth", "chatgpt_account_id")
	m.logf("oauth: tokens received access=%t refresh=%t id=%t account_id=%t", accessToken != "", refreshToken != "", idToken != "", accountID != "")

	bundle := authBundle{
		tokens: TokenData{
			IDToken:      idToken,
			AccessToken:  accessToken,
			RefreshToken: refreshToken,
			AccountID:    accountID,
		},
	}

	apiKey, err := m.maybeObtainAPIKey(ctx, idClaims, accessClaims, bundle.tokens)
	if err != nil {
		return nil, err
	}
	bundle.apiKey = apiKey

	return &AuthFile{
		OpenAIAPIKey: bundle.apiKey,
		Tokens:       bundle.tokens,
		LastRefresh:  m.now().UTC(),
	}, nil
}

func (m *LoginManager) maybeObtainAPIKey(ctx context.Context, idClaims, accessClaims map[string]any, tokens TokenData) (*string, error) {
	orgID := stringValue(idClaims["organization_id"])
	projectID := stringValue(idClaims["project_id"])
	if orgID == "" || projectID == "" {
		m.logf("oauth: skipping api-key token exchange (organization_id/project_id missing)")
		return nil, nil
	}
	m.logf("oauth: requesting api-key token exchange for org=%s project=%s", orgID, projectID)

	name := fmt.Sprintf("ChatGPT Local [auto-generated] (%s)", m.now().UTC().Format("2006-01-02"))
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:token-exchange")
	form.Set("client_id", m.clientID)
	form.Set("requested_token", "openai-api-key")
	form.Set("subject_token", tokens.IDToken)
	form.Set("subject_token_type", "urn:ietf:params:oauth:token-type:id_token")
	form.Set("name", name)

	_ = stringValue(accessClaims["chatgpt_plan_type"])
	exchangeResp, err := m.postForm(ctx, form)
	if err != nil {
		return nil, err
	}
	key := stringValue(exchangeResp["access_token"])
	if key == "" {
		m.logf("oauth: token exchange returned empty api key")
		return nil, nil
	}
	m.logf("oauth: token exchange returned api key")
	return &key, nil
}

func (m *LoginManager) postForm(ctx context.Context, form url.Values) (map[string]any, error) {
	grantType := form.Get("grant_type")
	m.logf("oauth: POST %s grant_type=%s", m.tokenURL, grantType)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauth post: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read oauth response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("oauth http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode oauth response: %w", err)
	}
	m.logf("oauth: grant_type=%s completed, response keys=%v", grantType, mapKeys(out))
	return out, nil
}

func RunLogin(ctx context.Context, opts LoginOptions) (int, error) {
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	stdin := opts.Stdin
	if stdin == nil {
		stdin = os.Stdin
	}

	bindHost := strings.TrimSpace(opts.BindHost)
	if bindHost == "" {
		bindHost = "127.0.0.1"
	}
	port := opts.Port
	if port == 0 {
		port = RequiredPort
	}

	issuer := strings.TrimSpace(opts.Issuer)
	if issuer == "" {
		issuer = DefaultIssuer
	}

	clientID := strings.TrimSpace(opts.ClientID)
	if clientID == "" {
		clientID = DefaultClientID
	}
	if clientID == "" {
		return 1, fmt.Errorf("no OAuth client id configured")
	}

	home := strings.TrimSpace(opts.HomeDir)
	if home == "" {
		home = authHomeDir()
	}
	authPath := strings.TrimSpace(opts.AuthFile)
	if authPath == "" {
		authPath = authFilePath(home)
	} else {
		home = filepath.Dir(authPath)
	}

	redirectURI := fmt.Sprintf("http://localhost:%d/auth/callback", port)
	manager, err := NewLoginManager(issuer, clientID, redirectURI, nil)
	if err != nil {
		return 1, err
	}
	manager.verbose = opts.Verbose
	manager.logf = func(format string, args ...any) {
		if manager.verbose {
			fmt.Fprintf(stderr, "[verbose] "+format+"\n", args...)
		}
	}
	authURL := manager.AuthURL()
	manager.logf("login config: bind=%s port=%d issuer=%s client_id=%s redirect_uri=%s auth_file=%s", bindHost, port, issuer, clientID, redirectURI, authPath)

	mux := http.NewServeMux()
	server := &http.Server{Addr: fmt.Sprintf("%s:%d", bindHost, port), Handler: mux}

	var (
		exitCode int = 1
		once     sync.Once
	)
	complete := func(code int) {
		once.Do(func() {
			exitCode = code
			ctxShutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = server.Shutdown(ctxShutdown)
		})
	}

	mux.HandleFunc("/success", func(w http.ResponseWriter, r *http.Request) {
		writeLoginSuccess(w)
		go func() {
			time.Sleep(2 * time.Second)
			complete(0)
		}()
	})
	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		manager.logf("callback hit: path=%s remote=%s", r.URL.Path, r.RemoteAddr)
		if r.Method != http.MethodGet {
			http.Error(w, "Not Found", http.StatusNotFound)
			complete(1)
			return
		}
		code := strings.TrimSpace(r.URL.Query().Get("code"))
		if code == "" {
			http.Error(w, "Missing auth code", http.StatusBadRequest)
			complete(1)
			return
		}
		if state := strings.TrimSpace(r.URL.Query().Get("state")); state != "" && state != manager.State() {
			manager.logf("callback state mismatch: expected=%s got=%s", manager.State(), state)
			http.Error(w, "State mismatch", http.StatusBadRequest)
			complete(1)
			return
		}
		auth, err := manager.ExchangeCode(r.Context(), code)
		if err != nil {
			http.Error(w, "Token exchange failed: "+err.Error(), http.StatusInternalServerError)
			complete(1)
			return
		}
		if err := writeAuthFilePath(authPath, auth); err != nil {
			http.Error(w, "Unable to persist auth file", http.StatusInternalServerError)
			complete(1)
			return
		}
		printSavedAuthSummary(stderr, authPath, auth)
		writeLoginSuccess(w)
		go func() {
			time.Sleep(2 * time.Second)
			complete(0)
		}()
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Not Found", http.StatusNotFound)
		complete(1)
	})

	if opts.Verbose {
		fmt.Fprintf(stderr, "Starting local login server on http://localhost:%d\n", port)
	}
	fmt.Fprintf(stderr, "If your browser did not open, navigate to:\n%s\n", authURL)
	if !opts.NoBrowser {
		if err := openBrowser(authURL); err != nil {
			manager.logf("open browser failed: %v", err)
		} else {
			manager.logf("browser open command started")
		}
	}
	fmt.Fprintln(stderr, "If the browser can't reach this machine, paste the full redirect URL here and press Enter (or leave blank to keep waiting):")

	go func() {
		line, _ := readSingleLine(ctx, stdin)
		line = strings.TrimSpace(line)
		if line == "" {
			return
		}
		u, err := url.Parse(line)
		if err != nil {
			fmt.Fprintf(stderr, "Failed to process pasted redirect URL: %v\n", err)
			return
		}
		code := strings.TrimSpace(u.Query().Get("code"))
		if code == "" {
			fmt.Fprintln(stderr, "Input did not contain an auth code. Ignoring.")
			return
		}
		if state := strings.TrimSpace(u.Query().Get("state")); state != "" && state != manager.State() {
			fmt.Fprintln(stderr, "State mismatch. Ignoring pasted URL for safety.")
			return
		}
		fmt.Fprintln(stderr, "Received redirect URL. Completing login without callback...")
		manager.logf("processing pasted redirect url")
		auth, err := manager.ExchangeCode(context.Background(), code)
		if err != nil {
			fmt.Fprintf(stderr, "Failed to process pasted redirect URL: %v\n", err)
			return
		}
		if err := writeAuthFilePath(authPath, auth); err != nil {
			fmt.Fprintf(stderr, "ERROR: Unable to persist auth file: %v\n", err)
			return
		}
		printSavedAuthSummary(stderr, authPath, auth)
		complete(0)
	}()

	errCh := make(chan error, 1)
	ln, err := net.Listen("tcp", server.Addr)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) || strings.Contains(strings.ToLower(err.Error()), "address already in use") {
			return 13, err
		}
		return 1, err
	}
	go func() {
		manager.logf("server listening on %s", server.Addr)
		errCh <- server.Serve(ln)
	}()

	select {
	case <-ctx.Done():
		complete(1)
		return exitCode, ctx.Err()
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return exitCode, nil
		}
		return 1, err
	}
}

func readSingleLine(ctx context.Context, r io.Reader) (string, error) {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		reader := bufio.NewReader(io.LimitReader(r, 16*1024))
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				ch <- result{line, nil}
				return
			}
			ch <- result{line, err}
			return
		}
		line = strings.TrimSuffix(line, "\n")
		ch <- result{line, nil}
	}()

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case res := <-ch:
		return res.line, res.err
	}
}

func writeLoginSuccess(w http.ResponseWriter) {
	body := []byte(loginSuccessHTML)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func writeAuthFile(home string, data *AuthFile) error {
	return writeAuthFilePath(authFilePath(home), data)
}

func writeAuthFilePath(path string, data *AuthFile) error {
	if data == nil {
		return fmt.Errorf("auth data is required")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("auth file path is required")
	}
	dir := filepath.Dir(path)
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("auth file directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create auth home directory %s: %w", dir, err)
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal auth file: %w", err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("write auth file: %w", err)
	}
	return nil
}

func authHomeDir() string {
	return authstore.DefaultAuthHomeDir()
}

func authFilePath(home string) string {
	return authstore.AuthFilePath(home)
}

func printSavedAuthSummary(w io.Writer, filePath string, auth *AuthFile) {
	if auth == nil {
		fmt.Fprintf(w, "Saved auth file: %s\n", filePath)
		return
	}
	fmt.Fprintf(w, "Login successful. Saved auth file: %s\n", filePath)
	fmt.Fprintf(w, "Saved fields: OPENAI_API_KEY=%t, tokens.id_token=%t, tokens.access_token=%t, tokens.refresh_token=%t, tokens.account_id=%t\n",
		auth.OpenAIAPIKey != nil && strings.TrimSpace(*auth.OpenAIAPIKey) != "",
		strings.TrimSpace(auth.Tokens.IDToken) != "",
		strings.TrimSpace(auth.Tokens.AccessToken) != "",
		strings.TrimSpace(auth.Tokens.RefreshToken) != "",
		strings.TrimSpace(auth.Tokens.AccountID) != "",
	)
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func generatePKCE() (pkceCodes, error) {
	verifier, err := tokenHex(64)
	if err != nil {
		return pkceCodes{}, err
	}
	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	return pkceCodes{codeVerifier: verifier, codeChallenge: challenge}, nil
}

func tokenHex(n int) (string, error) {
	if n <= 0 {
		return "", fmt.Errorf("invalid token length: %d", n)
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func parseJWTClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil
	}
	return claims
}

func nestedString(m map[string]any, keys ...string) string {
	if len(keys) == 0 {
		return ""
	}
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur, ok = mm[k]
		if !ok {
			return ""
		}
	}
	return stringValue(cur)
}

func stringValue(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func openBrowser(rawURL string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", rawURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	default:
		cmd = exec.Command("xdg-open", rawURL)
	}
	return cmd.Start()
}
