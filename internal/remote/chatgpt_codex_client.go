package remote

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultChatGPTBaseURL = "https://chatgpt.com"
const defaultRequestTimeout = 600 * time.Second

// HTTPError is returned when the upstream responds with a non-2xx status.
// The response body is included (up to the full body read) for debugging.
type HTTPError struct {
	URL        string
	StatusCode int
	Body       []byte
}

func (e *HTTPError) Error() string {
	b := strings.TrimSpace(string(e.Body))
	if b == "" {
		return fmt.Sprintf("upstream http %d: %s", e.StatusCode, e.URL)
	}
	return fmt.Sprintf("upstream http %d: %s: %s", e.StatusCode, e.URL, b)
}

// ChatGPTCodexClient sends requests to ChatGPT's backend-api Codex endpoint.
// It targets: POST /backend-api/codex/responses
type ChatGPTCodexClient struct {
	baseURL    string
	httpClient *http.Client
}

func NewChatGPTCodexClient(baseURL string, httpClient *http.Client) *ChatGPTCodexClient {
	b := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if b == "" {
		b = defaultChatGPTBaseURL
	}

	// http.Client.Timeout limits the whole request including streaming reads. Default to 10 minutes,
	// as required by the ChatGPT backend-api codex responses endpoint usage.
	var hc *http.Client
	switch {
	case httpClient == nil:
		hc = &http.Client{Timeout: defaultRequestTimeout}
	case httpClient.Timeout == 0:
		tmp := *httpClient
		tmp.Timeout = defaultRequestTimeout
		hc = &tmp
	default:
		hc = httpClient
	}

	return &ChatGPTCodexClient{
		baseURL:    b,
		httpClient: hc,
	}
}

// SetTimeout configures the underlying http.Client timeout (0 disables).
func (c *ChatGPTCodexClient) SetTimeout(d time.Duration) {
	if c == nil || c.httpClient == nil {
		return
	}
	c.httpClient.Timeout = d
}

// DoCodexResponses sends a POST to https://chatgpt.com/backend-api/codex/responses.
//
// The request includes mandatory headers:
// - Authorization: Bearer <access_token>
// - chatgpt-account-id: <account_id>
// - Accept: text/event-stream
// - OpenAI-Beta: responses=experimental
// - session_id: <session_id>
// - Content-Type: application/json
//
// On success (2xx), it returns the live *http.Response and leaves resp.Body open (caller must Close).
// For non-2xx, it reads the response body for debugging and returns *HTTPError.
func (c *ChatGPTCodexClient) DoCodexResponses(ctx context.Context, accessToken, accountID, sessionID string, payload any) (*http.Response, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, fmt.Errorf("accessToken is required")
	}
	if strings.TrimSpace(accountID) == "" {
		return nil, fmt.Errorf("accountID is required")
	}
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("sessionID is required")
	}

	var bodyBytes []byte
	switch v := payload.(type) {
	case nil:
		bodyBytes = []byte("{}")
	case []byte:
		bodyBytes = v
	case json.RawMessage:
		bodyBytes = []byte(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("marshal payload: %w", err)
		}
		bodyBytes = b
	}

	url := c.baseURL + "/backend-api/codex/responses"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("chatgpt-account-id", strings.TrimSpace(accountID))
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("session_id", strings.TrimSpace(sessionID))
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, fmt.Errorf("read response: %w", readErr)
		}
		return nil, &HTTPError{URL: url, StatusCode: resp.StatusCode, Body: respBody}
	}
	return resp, nil
}

// ReadSSELines reads a text/event-stream response line-by-line and calls onLine for each line.
// Lines are passed without the trailing newline. Empty lines are reported as "".
func ReadSSELines(ctx context.Context, r io.Reader, onLine func(line string) error) error {
	if onLine == nil {
		return fmt.Errorf("onLine is required")
	}

	sc := bufio.NewScanner(r)
	// SSE "data:" lines can exceed the default 64K; allow up to 1MB per line.
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)

	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := onLine(sc.Text()); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return ctx.Err()
}
