package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"brainhub/internal/authstore"

	"github.com/google/uuid"
)

var (
	model = "gpt-5.3-codex"
)

func TestChatGPTCodexClient_DoCodexResponses(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real upstream test in -short mode")
	}
	auth := mustLoadAuthFileForIntegration(t)
	sessionID := uuid.NewString()

	body, err := BuildCodexResponsesRequest(sessionID, CodexResponsesRequest{
		Model:             model,
		Instructions:      "Respond with 'pong' to the input.",
		Input:             []any{map[string]any{"type": "input_text", "text": "ping"}},
		Tools:             []any{},
		ToolChoice:        nil,   // should normalize to "auto"
		ParallelToolCalls: false, // caller-controlled
		Reasoning: &ReasoningParam{
			Effort:  "medium",
			Summary: "auto",
		}, // off by default in this test
	})
	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	c := NewChatGPTCodexClient("https://chatgpt.com", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := c.DoCodexResponses(ctx, auth.Tokens.AccessToken, auth.Tokens.AccountID, sessionID, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if ct != "" && !strings.Contains(ct, "text/event-stream") {
		t.Logf("warning: unexpected content-type=%q (expected text/event-stream)", ct)
	}

	// Attempt to read at least one SSE line quickly; don't hang the test if the stream is slow.
	stop := errors.New("stop")
	sseCtx, sseCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer sseCancel()
	_ = ReadSSELines(sseCtx, resp.Body, func(line string) error {
		if strings.TrimSpace(line) == "" {
			return nil
		}
		t.Logf("first sse line: %q", line)
		return stop
	})
}

func TestChatGPTCodexClient_DoCodexResponses_Headers(t *testing.T) {
	var got struct {
		Method     string
		Path       string
		Body       json.RawMessage
		Auth       string
		AccountID  string
		Accept     string
		Beta       string
		SessionID  string
		ContentTyp string
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Method = r.Method
		got.Path = r.URL.Path
		got.Auth = r.Header.Get("Authorization")
		got.AccountID = r.Header.Get("chatgpt-account-id")
		got.Accept = r.Header.Get("Accept")
		got.Beta = r.Header.Get("OpenAI-Beta")
		got.SessionID = r.Header.Get("session_id")
		got.ContentTyp = r.Header.Get("Content-Type")
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		got.Body = json.RawMessage(b)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := NewChatGPTCodexClient(srv.URL, srv.Client())
	resp, err := c.DoCodexResponses(context.Background(), "tok", "acc", "sid", map[string]any{"x": 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("body=%s", string(body))
	}

	if got.Method != http.MethodPost {
		t.Fatalf("method=%q", got.Method)
	}
	if got.Path != "/backend-api/codex/responses" {
		t.Fatalf("path=%q", got.Path)
	}
	if got.Auth != "Bearer tok" {
		t.Fatalf("authorization=%q", got.Auth)
	}
	if got.AccountID != "acc" {
		t.Fatalf("chatgpt-account-id=%q", got.AccountID)
	}
	if got.Accept != "text/event-stream" {
		t.Fatalf("accept=%q", got.Accept)
	}
	if got.Beta != "responses=experimental" {
		t.Fatalf("openai-beta=%q", got.Beta)
	}
	if got.SessionID != "sid" {
		t.Fatalf("session_id=%q", got.SessionID)
	}
	if got.ContentTyp != "application/json" {
		t.Fatalf("content-type=%q", got.ContentTyp)
	}
	if string(got.Body) != `{"x":1}` {
		t.Fatalf("body=%s", string(got.Body))
	}
}

func TestReadSSELines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)

		f, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", http.StatusInternalServerError)
			return
		}

		_, _ = fmt.Fprint(w, "data: one\n\n")
		f.Flush()
		_, _ = fmt.Fprint(w, "data: two\n\n")
		f.Flush()
	}))
	defer srv.Close()

	c := NewChatGPTCodexClient(srv.URL, srv.Client())
	resp, err := c.DoCodexResponses(context.Background(), "tok", "acc", "sid", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	var lines []string
	err = ReadSSELines(context.Background(), resp.Body, func(line string) error {
		lines = append(lines, line)
		return nil
	})
	if err != nil {
		t.Fatalf("ReadSSELines error: %v", err)
	}

	// Filter blank lines to keep the assertion stable.
	var nonEmpty []string
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			nonEmpty = append(nonEmpty, l)
		}
	}
	if strings.Join(nonEmpty, "|") != "data: one|data: two" {
		t.Fatalf("unexpected lines: %#v", nonEmpty)
	}
}

type integrationAuthFile struct {
	Tokens struct {
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
	} `json:"tokens"`
}

func mustLoadAuthFileForIntegration(t *testing.T) integrationAuthFile {
	t.Helper()

	path := authstore.DefaultAuthFilePath()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("skip integration test: cannot read %s: %v", path, err)
	}

	var out integrationAuthFile
	if err := json.Unmarshal(b, &out); err != nil {
		t.Skipf("skip integration test: invalid %s: %v", path, err)
	}
	if strings.TrimSpace(out.Tokens.AccessToken) == "" || strings.TrimSpace(out.Tokens.AccountID) == "" {
		t.Skipf("skip integration test: %s must contain tokens.access_token and tokens.account_id", path)
	}
	return out
}
