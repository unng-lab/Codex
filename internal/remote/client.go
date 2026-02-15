package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"chatmock/internal/chat"
)

type Provider struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	BaseURL     string `json:"base_url"`
	APIKey      string `json:"api_key,omitempty"`
	AccessToken string `json:"access_token,omitempty"`
	AccountID   string `json:"account_id,omitempty"`
	ModelPrefix string `json:"model_prefix"`
	RouteAll    bool   `json:"route_all"`
}

type ProviderView struct {
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	BaseURL        string `json:"base_url"`
	HasAPIKey      bool   `json:"has_api_key"`
	HasAccessToken bool   `json:"has_access_token"`
	HasAccountID   bool   `json:"has_account_id"`
	ModelPrefix    string `json:"model_prefix"`
	RouteAll       bool   `json:"route_all"`
}

type Manager struct {
	mu        sync.RWMutex
	providers []Provider
}

func NewManager(seed []Provider) *Manager {
	m := &Manager{}
	for _, p := range seed {
		m.Upsert(p)
	}
	return m
}

func (m *Manager) Upsert(provider Provider) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := normalizeProvider(provider)
	for i := range m.providers {
		if sameProvider(m.providers[i], p) {
			m.providers[i] = p
			return
		}
	}
	m.providers = append(m.providers, p)
}

func (m *Manager) SetAll(providers []Provider) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.providers = m.providers[:0]
	for _, p := range providers {
		m.providers = append(m.providers, normalizeProvider(p))
	}
}

func (m *Manager) Views() []ProviderView {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ProviderView, 0, len(m.providers))
	for _, p := range m.providers {
		out = append(out, ProviderView{
			Name:           p.Name,
			Kind:           p.Kind,
			BaseURL:        p.BaseURL,
			HasAPIKey:      strings.TrimSpace(p.APIKey) != "",
			HasAccessToken: strings.TrimSpace(p.AccessToken) != "",
			HasAccountID:   strings.TrimSpace(p.AccountID) != "",
			ModelPrefix:    p.ModelPrefix,
			RouteAll:       p.RouteAll,
		})
	}
	return out
}

func (m *Manager) Providers() []Provider {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Provider, len(m.providers))
	copy(out, m.providers)
	return out
}

func (m *Manager) Match(requestedModel string) (Provider, string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, p := range m.providers {
		if ShouldProxy(p, requestedModel) {
			return p, NormalizeModel(p, requestedModel), true
		}
	}
	return Provider{}, "", false
}

func sameProvider(a, b Provider) bool {
	if strings.TrimSpace(a.Name) != "" && strings.TrimSpace(b.Name) != "" {
		return a.Name == b.Name
	}
	if strings.TrimSpace(a.ModelPrefix) != "" && strings.TrimSpace(b.ModelPrefix) != "" {
		return a.ModelPrefix == b.ModelPrefix
	}
	return a.Kind == b.Kind && a.BaseURL == b.BaseURL
}

func normalizeProvider(p Provider) Provider {
	if strings.TrimSpace(p.Kind) == "" {
		p.Kind = "openai"
	}
	p.Kind = strings.ToLower(strings.TrimSpace(p.Kind))
	if strings.TrimSpace(p.ModelPrefix) == "" {
		switch p.Kind {
		case "ollama":
			p.ModelPrefix = "ollama/"
		case "codex":
			p.ModelPrefix = "codex/"
		case "chatgpt":
			p.ModelPrefix = "chatgpt/"
		default:
			p.ModelPrefix = "remote/"
		}
	}
	if strings.TrimSpace(p.Name) == "" {
		p.Name = strings.TrimSuffix(p.ModelPrefix, "/")
	}
	if strings.TrimSpace(p.BaseURL) == "" && p.Kind == "chatgpt" {
		p.BaseURL = "https://chatgpt.com"
	}
	return p
}

type Client struct {
	httpClient *http.Client
}

func NewClient() *Client {
	return &Client{httpClient: &http.Client{Timeout: 60 * time.Second}}
}

func (c *Client) ChatCompletions(ctx context.Context, provider Provider, req chat.CompletionRequest, model string) ([]byte, int, error) {
	switch provider.Kind {
	case "ollama":
		return c.chatOllama(ctx, provider, req, model)
	case "chatgpt":
		return c.chatChatGPT(ctx, provider, req, model)
	case "codex", "openai":
		fallthrough
	default:
		return c.chatOpenAICompatible(ctx, provider, req, model)
	}
}

func (c *Client) chatOpenAICompatible(ctx context.Context, provider Provider, req chat.CompletionRequest, model string) ([]byte, int, error) {
	url, err := joinURL(provider.BaseURL, "/v1/chat/completions")
	if err != nil {
		return nil, 0, err
	}
	payload := map[string]any{"model": model, "messages": req.Messages}
	if req.Temperature != 0 {
		payload["temperature"] = req.Temperature
	}
	return c.postJSON(ctx, provider, url, payload)
}

func (c *Client) chatChatGPT(ctx context.Context, provider Provider, req chat.CompletionRequest, model string) ([]byte, int, error) {
	url, err := joinURL(provider.BaseURL, "/backend-api/codex/responses")
	if err != nil {
		return nil, 0, err
	}
	input := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		input = append(input, map[string]any{
			"role":    m.Role,
			"content": []map[string]any{{"type": "input_text", "text": m.Content}},
		})
	}
	payload := map[string]any{"model": model, "input": input, "stream": false}

	body, status, err := c.postJSON(ctx, provider, url, payload)
	if err != nil {
		return nil, 0, err
	}
	if status >= 400 {
		return body, status, nil
	}

	text := extractChatGPTOutputText(body)
	if text == "" {
		text = ""
	}
	resp := chat.CompletionResponse{
		ID:      "chatcmpl-chatgpt",
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []chat.Choice{{
			Index:        0,
			FinishReason: "stop",
			Message:      chat.Message{Role: "assistant", Content: text},
		}},
		Usage: chat.Usage{},
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return nil, 0, err
	}
	return out, http.StatusOK, nil
}

func extractChatGPTOutputText(body []byte) string {
	var simple struct {
		OutputText string `json:"output_text"`
	}
	if err := json.Unmarshal(body, &simple); err == nil && strings.TrimSpace(simple.OutputText) != "" {
		return simple.OutputText
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return ""
	}
	if v, ok := raw["output_text"].(string); ok {
		return v
	}
	if out, ok := raw["output"].([]any); ok {
		chunks := make([]string, 0)
		for _, item := range out {
			obj, _ := item.(map[string]any)
			content, _ := obj["content"].([]any)
			for _, c := range content {
				co, _ := c.(map[string]any)
				if t, ok := co["text"].(string); ok {
					chunks = append(chunks, t)
				}
			}
		}
		return strings.TrimSpace(strings.Join(chunks, "\n"))
	}
	return ""
}

func (c *Client) chatOllama(ctx context.Context, provider Provider, req chat.CompletionRequest, model string) ([]byte, int, error) {
	url, err := joinURL(provider.BaseURL, "/api/chat")
	if err != nil {
		return nil, 0, err
	}
	payload := map[string]any{"model": model, "messages": req.Messages, "stream": false}
	body, status, err := c.postJSON(ctx, provider, url, payload)
	if err != nil {
		return nil, 0, err
	}
	if status >= 400 {
		return body, status, nil
	}

	var ollamaResp struct {
		Model   string `json:"model"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		PromptEvalCount int `json:"prompt_eval_count"`
		EvalCount       int `json:"eval_count"`
	}
	if err := json.Unmarshal(body, &ollamaResp); err != nil {
		return nil, 0, fmt.Errorf("decode ollama response: %w", err)
	}

	resp := chat.CompletionResponse{
		ID:      "chatcmpl-ollama",
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   ollamaResp.Model,
		Choices: []chat.Choice{{Index: 0, FinishReason: "stop", Message: chat.Message{Role: ollamaResp.Message.Role, Content: ollamaResp.Message.Content}}},
		Usage:   chat.Usage{PromptTokens: ollamaResp.PromptEvalCount, CompletionTokens: ollamaResp.EvalCount},
	}
	resp.Usage.TotalTokens = resp.Usage.PromptTokens + resp.Usage.CompletionTokens
	out, err := json.Marshal(resp)
	if err != nil {
		return nil, 0, err
	}
	return out, http.StatusOK, nil
}

func (c *Client) postJSON(ctx context.Context, provider Provider, url string, payload any) ([]byte, int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if token := strings.TrimSpace(provider.AccessToken); token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	} else if apiKey := strings.TrimSpace(provider.APIKey); apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if strings.TrimSpace(provider.AccountID) != "" {
		httpReq.Header.Set("chatgpt-account-id", provider.AccountID)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}
	return respBody, resp.StatusCode, nil
}

func joinURL(baseURL, suffix string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return "", fmt.Errorf("remote base_url is empty")
	}
	return base + suffix, nil
}

func ShouldProxy(provider Provider, requestedModel string) bool {
	if strings.TrimSpace(provider.BaseURL) == "" {
		return false
	}
	if provider.RouteAll {
		return true
	}
	prefix := strings.TrimSpace(provider.ModelPrefix)
	return prefix != "" && strings.HasPrefix(strings.TrimSpace(requestedModel), prefix)
}

func NormalizeModel(provider Provider, requestedModel string) string {
	model := strings.TrimSpace(requestedModel)
	prefix := strings.TrimSpace(provider.ModelPrefix)
	if prefix != "" && strings.HasPrefix(model, prefix) {
		return strings.TrimPrefix(model, prefix)
	}
	return model
}
