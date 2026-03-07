package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"brainhub/internal/remote"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

func TestProxy_Responses_AgentWithTool_RealEndpoints(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real upstream test in -short mode")
	}

	tokens, err := LoadAuthTokens(DefaultAuthFilePath())
	if err != nil {
		t.Skipf("skip integration test: load auth tokens: %v", err)
	}
	t.Logf("auth loaded: account_id=%s", maskValue(tokens.AccountID))

	logger := zaptest.NewLogger(t, zaptest.Level(zap.DebugLevel))

	h, err := NewServer(Config{
		AccessToken: tokens.AccessToken,
		AccountID:   tokens.AccountID,
		BaseURL:     "https://chatgpt.com",
	}, logger)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	srv := &http.Server{
		Handler:      h,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0,
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	go func() {
		_ = srv.Serve(ln)
	}()
	baseURL := "http://" + ln.Addr().String()
	t.Logf("proxy test server started: %s", baseURL)

	payload := map[string]any{
		"model":        "gpt-5.3-codex",
		"instructions": "You are an agent. You must call the provided function tool exactly once before finalizing.",
		"input":        "Call the tool to get the current Go release and then summarize in one sentence.",
		"tools": []any{
			map[string]any{
				"type":        "function",
				"name":        "lookup_go_release",
				"description": "Returns the current Go release string.",
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
		},
		"tool_choice": map[string]any{
			"type": "function",
			"name": "lookup_go_release",
		},
		"stream": true,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	t.Logf("sending llm request: model=%v stream=%v", payload["model"], payload["stream"])

	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/responses", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type=%q", ct)
	}
	t.Logf("llm stream connected: status=%d content-type=%s", resp.StatusCode, resp.Header.Get("Content-Type"))

	var (
		sawToolCall bool
		seenData    []string
	)
	stop := errors.New("stop after tool call")
	readErr := remote.ReadSSELines(ctx, resp.Body, func(line string) error {
		if strings.TrimSpace(line) != "" {
			t.Logf("llm sse line: %s", line)
		}
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data:") {
			return nil
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			return nil
		}
		t.Logf("llm sse data: %s", data)
		if len(seenData) < 12 {
			seenData = append(seenData, data)
		}

		var event map[string]any
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return nil
		}
		if hasToolCallSignal(event) {
			sawToolCall = true
			return stop
		}
		return nil
	})
	if readErr != nil && !errors.Is(readErr, stop) {
		t.Fatalf("read sse: %v", readErr)
	}
	if !sawToolCall {
		t.Fatalf("expected at least one tool-call signal in SSE; seen=%v", seenData)
	}
	t.Logf("tool call detected; integration flow is valid")
}

func hasToolCallSignal(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, vv := range x {
			lk := strings.ToLower(strings.TrimSpace(k))
			if (lk == "type" || lk == "event") && isToolCallString(vv) {
				return true
			}
			if hasToolCallSignal(vv) {
				return true
			}
		}
	case []any:
		for _, item := range x {
			if hasToolCallSignal(item) {
				return true
			}
		}
	}
	return false
}

func isToolCallString(v any) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return false
	}
	return strings.Contains(s, "tool_call") ||
		strings.Contains(s, "function_call") ||
		strings.Contains(s, "web_search_call") ||
		strings.Contains(s, "computer_call") ||
		strings.Contains(s, "code_interpreter_call") ||
		strings.Contains(s, "mcp_call")
}
