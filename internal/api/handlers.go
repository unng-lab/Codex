package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"chatmock/internal/chat"
	"chatmock/internal/remote"
	"chatmock/internal/rules"
)

type Handlers struct {
	rules         *rules.Store
	remoteManager *remote.Manager
	remoteClient  *remote.Client
}

func NewHandlers(store *rules.Store, manager *remote.Manager) *Handlers {
	if manager == nil {
		manager = remote.NewManager(nil)
	}
	return &Handlers{rules: store, remoteManager: manager, remoteClient: remote.NewClient()}
}

func (h *Handlers) Health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handlers) Rules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"rules": h.rules.All()})
	case http.MethodPut:
		var payload struct {
			Rules []rules.Rule `json:"rules"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON payload"})
			return
		}
		h.rules.Set(payload.Rules)
		writeJSON(w, http.StatusOK, map[string]any{"rules": h.rules.All()})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *Handlers) Providers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"providers": h.remoteManager.Views()})
	case http.MethodPut:
		var payload struct {
			Provider  *remote.Provider  `json:"provider"`
			Providers []remote.Provider `json:"providers"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON payload"})
			return
		}
		switch {
		case len(payload.Providers) > 0:
			h.remoteManager.SetAll(payload.Providers)
		case payload.Provider != nil:
			h.remoteManager.Upsert(*payload.Provider)
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provider or providers is required"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"providers": h.remoteManager.Views()})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *Handlers) Models(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, h.buildModelsResponse())
}

func (h *Handlers) Responses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req chat.ResponsesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON payload"})
		return
	}
	ccReq := chat.CompletionRequest{Model: req.Model, Messages: []chat.Message{{Role: "user", Content: flattenInput(req.Input)}}}
	ccResp, status, err := h.runCompletion(r, ccReq)
	if err != nil {
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	output := ""
	if len(ccResp.Choices) > 0 {
		output = ccResp.Choices[0].Message.Content
	}
	writeJSON(w, http.StatusOK, chat.ResponsesResponse{ID: "resp-mock", Object: "response", CreatedAt: time.Now().Unix(), Model: ccResp.Model, OutputText: output})
}

func (h *Handlers) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req chat.CompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON payload"})
		return
	}
	resp, status, err := h.runCompletion(r, req)
	if err != nil {
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handlers) Completions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Model  string `json:"model"`
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON payload"})
		return
	}
	ccResp, status, err := h.runCompletion(r, chat.CompletionRequest{Model: req.Model, Messages: []chat.Message{{Role: "user", Content: req.Prompt}}})
	if err != nil {
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	text := ""
	if len(ccResp.Choices) > 0 {
		text = ccResp.Choices[0].Message.Content
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      ccResp.ID,
		"object":  "text_completion",
		"created": ccResp.Created,
		"model":   ccResp.Model,
		"choices": []map[string]any{{"index": 0, "text": text, "finish_reason": "stop"}},
	})
}

func (h *Handlers) OllamaChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Model    string         `json:"model"`
		Messages []chat.Message `json:"messages"`
		Stream   bool           `json:"stream"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON payload"})
		return
	}
	resp, status, err := h.runCompletion(r, chat.CompletionRequest{Model: req.Model, Messages: req.Messages})
	if err != nil {
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	content := ""
	if len(resp.Choices) > 0 {
		content = resp.Choices[0].Message.Content
	}
	if req.Stream {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, fmt.Sprintf("{\"model\":%q,\"message\":{\"role\":\"assistant\",\"content\":%q},\"done\":false}\n", chooseModel(resp.Model), content))
		_, _ = io.WriteString(w, fmt.Sprintf("{\"model\":%q,\"done\":true}\n", chooseModel(resp.Model)))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"model":      chooseModel(resp.Model),
		"created_at": time.Now().UTC().Format(time.RFC3339),
		"message":    map[string]any{"role": "assistant", "content": content},
		"done":       true,
	})
}

func (h *Handlers) OllamaTags(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	models := make([]map[string]any, 0)
	for _, m := range h.buildModelsResponse().Data {
		models = append(models, map[string]any{"name": m.ID, "model": m.ID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

func (h *Handlers) OllamaShow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	writeJSON(w, http.StatusOK, map[string]any{"modelfile": "# mock", "parameters": "", "template": "", "details": map[string]any{"family": "chatmock"}, "model_info": map[string]any{"name": req.Name}})
}

func (h *Handlers) OllamaVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"version": "0.1.0-chatmock"})
}

func (h *Handlers) runCompletion(r *http.Request, req chat.CompletionRequest) (chat.CompletionResponse, int, error) {
	if len(req.Messages) == 0 {
		return chat.CompletionResponse{}, http.StatusBadRequest, fmt.Errorf("messages must not be empty")
	}
	if provider, model, ok := h.remoteManager.Match(req.Model); ok {
		respBody, status, err := h.remoteClient.ChatCompletions(r.Context(), provider, req, model)
		if err != nil {
			return chat.CompletionResponse{}, http.StatusBadGateway, fmt.Errorf("remote request failed: %w", err)
		}
		if status >= 400 {
			return chat.CompletionResponse{}, status, fmt.Errorf("remote returned status %d", status)
		}
		var out chat.CompletionResponse
		if err := json.Unmarshal(respBody, &out); err != nil {
			return chat.CompletionResponse{}, http.StatusBadGateway, fmt.Errorf("invalid remote response")
		}
		return out, http.StatusOK, nil
	}
	last := req.Messages[len(req.Messages)-1].Content
	reply, ok := h.rules.Match(last)
	if !ok {
		reply = "Mock response: I received your message and no custom rule matched."
	}
	resp := chat.CompletionResponse{ID: "chatcmpl-mock", Object: "chat.completion", Created: time.Now().Unix(), Model: chooseModel(req.Model), Choices: []chat.Choice{{Index: 0, FinishReason: "stop", Message: chat.Message{Role: "assistant", Content: reply}}}, Usage: chat.Usage{PromptTokens: estimateTokens(req.Messages), CompletionTokens: estimateTokens([]chat.Message{{Role: "assistant", Content: reply}})}}
	resp.Usage.TotalTokens = resp.Usage.PromptTokens + resp.Usage.CompletionTokens
	return resp, http.StatusOK, nil
}

func (h *Handlers) buildModelsResponse() chat.ModelsResponse {
	created := time.Now().Unix()
	models := []chat.ModelInfo{{ID: "gpt-mock-1", Object: "model", Created: created, OwnedBy: "chatmock"}}
	for _, p := range h.remoteManager.Providers() {
		id := strings.TrimSuffix(p.ModelPrefix, "/") + "/*"
		if strings.TrimSpace(p.ModelPrefix) == "" {
			id = p.Name
		}
		models = append(models, chat.ModelInfo{ID: id, Object: "model", Created: created, OwnedBy: p.Kind})
	}
	return chat.ModelsResponse{Object: "list", Data: models}
}

func flattenInput(input any) string {
	switch v := input.(type) {
	case string:
		return v
	case []any:
		parts := make([]string, 0, len(v))
		for _, it := range v {
			parts = append(parts, flattenInput(it))
		}
		return strings.TrimSpace(strings.Join(parts, " "))
	case map[string]any:
		if txt, ok := v["text"].(string); ok {
			return txt
		}
		if c, ok := v["content"]; ok {
			return flattenInput(c)
		}
		return ""
	default:
		return ""
	}
}

func chooseModel(model string) string {
	if strings.TrimSpace(model) == "" {
		return "gpt-mock-1"
	}
	return model
}

func estimateTokens(messages []chat.Message) int {
	total := 0
	for _, m := range messages {
		total += len(m.Content)
	}
	if total == 0 {
		return 0
	}
	if total < 4 {
		return 1
	}
	return total / 4
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
