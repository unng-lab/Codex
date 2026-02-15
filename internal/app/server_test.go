package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"chatmock/internal/chat"
	"chatmock/internal/rules"
)

func TestHealthz(t *testing.T) {
	srv := NewServer()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()

	srv.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestModelsContainsMock(t *testing.T) {
	srv := NewServer()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rr := httptest.NewRecorder()

	srv.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("gpt-mock-1")) {
		t.Fatalf("missing mock model: %s", rr.Body.String())
	}
}

func TestResponsesEndpoint(t *testing.T) {
	srv := NewServer()
	payload := map[string]any{
		"model": "gpt-mock-1",
		"input": "hello",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	rr := httptest.NewRecorder()

	srv.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("output_text")) {
		t.Fatalf("unexpected response payload: %s", rr.Body.String())
	}
}

func TestChatCompletionWithDefaultRule(t *testing.T) {
	srv := NewServer()
	payload := chat.CompletionRequest{Messages: []chat.Message{{Role: "user", Content: "hello there"}}}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp chat.CompletionResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
	}
}

func TestRulesUpdate(t *testing.T) {
	srv := NewServer()
	update := map[string]any{"rules": []rules.Rule{{Contains: "pizza", Reply: "Mock says: pizza time."}}}
	body, _ := json.Marshal(update)

	updateReq := httptest.NewRequest(http.MethodPut, "/v1/rules", bytes.NewReader(body))
	updateRR := httptest.NewRecorder()
	srv.Routes().ServeHTTP(updateRR, updateReq)

	if updateRR.Code != http.StatusOK {
		t.Fatalf("expected 200 on rule update, got %d", updateRR.Code)
	}
}

func TestOllamaProviderProxy(t *testing.T) {
	ollamaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload["model"] != "llama3.1" {
			t.Fatalf("expected stripped model, got %v", payload["model"])
		}
		_, _ = w.Write([]byte(`{"model":"llama3.1","message":{"role":"assistant","content":"from ollama"},"prompt_eval_count":2,"eval_count":3}`))
	}))
	defer ollamaSrv.Close()

	srv := NewServer()
	providerPayload := map[string]any{"providers": []map[string]any{{
		"name":         "ollama",
		"kind":         "ollama",
		"base_url":     ollamaSrv.URL,
		"model_prefix": "ollama/",
	}}}
	providerBody, _ := json.Marshal(providerPayload)
	providerReq := httptest.NewRequest(http.MethodPut, "/v1/providers", bytes.NewReader(providerBody))
	providerRR := httptest.NewRecorder()
	srv.Routes().ServeHTTP(providerRR, providerReq)

	chatPayload := map[string]any{"model": "ollama/llama3.1", "messages": []map[string]string{{"role": "user", "content": "hi"}}}
	chatBody, _ := json.Marshal(chatPayload)
	chatReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(chatBody))
	chatRR := httptest.NewRecorder()
	srv.Routes().ServeHTTP(chatRR, chatReq)

	if chatRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", chatRR.Code, chatRR.Body.String())
	}
	if !bytes.Contains(chatRR.Body.Bytes(), []byte("from ollama")) {
		t.Fatalf("expected ollama response, got %s", chatRR.Body.String())
	}
}

func TestCodexProviderProxy(t *testing.T) {
	codexSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("missing auth header, got: %s", r.Header.Get("Authorization"))
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload["model"] != "gpt-5-codex" {
			t.Fatalf("expected stripped model, got %v", payload["model"])
		}
		_, _ = w.Write([]byte(`{"id":"codex-1","object":"chat.completion","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"from codex"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer codexSrv.Close()

	srv := NewServer()
	providerPayload := map[string]any{"providers": []map[string]any{{
		"name":         "codex",
		"kind":         "codex",
		"base_url":     codexSrv.URL,
		"api_key":      "test-key",
		"model_prefix": "codex/",
	}}}
	providerBody, _ := json.Marshal(providerPayload)
	providerReq := httptest.NewRequest(http.MethodPut, "/v1/providers", bytes.NewReader(providerBody))
	providerRR := httptest.NewRecorder()
	srv.Routes().ServeHTTP(providerRR, providerReq)

	chatPayload := map[string]any{"model": "codex/gpt-5-codex", "messages": []map[string]string{{"role": "user", "content": "hi"}}}
	chatBody, _ := json.Marshal(chatPayload)
	chatReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(chatBody))
	chatRR := httptest.NewRecorder()
	srv.Routes().ServeHTTP(chatRR, chatReq)

	if chatRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", chatRR.Code, chatRR.Body.String())
	}
	if !bytes.Contains(chatRR.Body.Bytes(), []byte("from codex")) {
		t.Fatalf("expected codex response, got %s", chatRR.Body.String())
	}
}

func TestProvidersGetMasksAPIKey(t *testing.T) {
	srv := NewServer()
	providerPayload := map[string]any{"providers": []map[string]any{{
		"name":         "codex",
		"kind":         "codex",
		"base_url":     "https://example.com",
		"api_key":      "secret",
		"model_prefix": "codex/",
	}}}
	providerBody, _ := json.Marshal(providerPayload)
	providerReq := httptest.NewRequest(http.MethodPut, "/v1/providers", bytes.NewReader(providerBody))
	providerRR := httptest.NewRecorder()
	srv.Routes().ServeHTTP(providerRR, providerReq)

	getReq := httptest.NewRequest(http.MethodGet, "/v1/providers", nil)
	getRR := httptest.NewRecorder()
	srv.Routes().ServeHTTP(getRR, getReq)

	if getRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", getRR.Code)
	}
	if bytes.Contains(getRR.Body.Bytes(), []byte("secret")) {
		t.Fatalf("api key leaked in response: %s", getRR.Body.String())
	}
	if !bytes.Contains(getRR.Body.Bytes(), []byte("\"has_api_key\":true")) {
		t.Fatalf("expected has_api_key=true, got %s", getRR.Body.String())
	}
}

func TestCompletionsEndpoint(t *testing.T) {
	srv := NewServer()
	payload := map[string]any{"model": "gpt-mock-1", "prompt": "hello"}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/v1/completions", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("text_completion")) {
		t.Fatalf("unexpected response: %s", rr.Body.String())
	}
}

func TestOllamaCompatEndpoints(t *testing.T) {
	srv := NewServer()
	chatPayload := map[string]any{"model": "gpt-mock-1", "messages": []map[string]string{{"role": "user", "content": "hello"}}, "stream": false}
	chatBody, _ := json.Marshal(chatPayload)
	chatReq := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewReader(chatBody))
	chatRR := httptest.NewRecorder()
	srv.Routes().ServeHTTP(chatRR, chatReq)
	if chatRR.Code != http.StatusOK {
		t.Fatalf("/api/chat expected 200, got %d", chatRR.Code)
	}

	tagsReq := httptest.NewRequest(http.MethodGet, "/api/tags", nil)
	tagsRR := httptest.NewRecorder()
	srv.Routes().ServeHTTP(tagsRR, tagsReq)
	if tagsRR.Code != http.StatusOK {
		t.Fatalf("/api/tags expected 200, got %d", tagsRR.Code)
	}

	verReq := httptest.NewRequest(http.MethodGet, "/api/version", nil)
	verRR := httptest.NewRecorder()
	srv.Routes().ServeHTTP(verRR, verReq)
	if verRR.Code != http.StatusOK {
		t.Fatalf("/api/version expected 200, got %d", verRR.Code)
	}
}

func TestChatGPTProviderProxy(t *testing.T) {
	chatgptSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/codex/responses" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer chatgpt-token" {
			t.Fatalf("missing bearer token: %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("chatgpt-account-id") != "acct-1" {
			t.Fatalf("missing account id: %s", r.Header.Get("chatgpt-account-id"))
		}
		_, _ = w.Write([]byte(`{"output":[{"content":[{"text":"from chatgpt"}]}]}`))
	}))
	defer chatgptSrv.Close()

	srv := NewServer()
	providerPayload := map[string]any{"providers": []map[string]any{{
		"name": "chatgpt", "kind": "chatgpt", "base_url": chatgptSrv.URL,
		"access_token": "chatgpt-token", "account_id": "acct-1", "model_prefix": "chatgpt/",
	}}}
	providerBody, _ := json.Marshal(providerPayload)
	providerReq := httptest.NewRequest(http.MethodPut, "/v1/providers", bytes.NewReader(providerBody))
	providerRR := httptest.NewRecorder()
	srv.Routes().ServeHTTP(providerRR, providerReq)

	chatPayload := map[string]any{"model": "chatgpt/gpt-5", "messages": []map[string]string{{"role": "user", "content": "hi"}}}
	chatBody, _ := json.Marshal(chatPayload)
	chatReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(chatBody))
	chatRR := httptest.NewRecorder()
	srv.Routes().ServeHTTP(chatRR, chatReq)

	if chatRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", chatRR.Code, chatRR.Body.String())
	}
	if !bytes.Contains(chatRR.Body.Bytes(), []byte("from chatgpt")) {
		t.Fatalf("expected chatgpt response, got %s", chatRR.Body.String())
	}
}
