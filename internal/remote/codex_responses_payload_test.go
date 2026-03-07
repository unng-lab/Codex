package remote

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func TestBuildCodexResponsesRequest_Defaults(t *testing.T) {
	req, err := BuildCodexResponsesRequest("sid", CodexResponsesRequest{
		Model:             "gpt-4.1",
		Instructions:      "Respond with 'pong' to the input.",
		Input:             []any{map[string]any{"type": "input_text", "text": "hi"}},
		Tools:             nil,
		ToolChoice:        nil,
		ParallelToolCalls: false,
		Store:             true,
		Stream:            false,
		PromptCacheKey:    "wrong",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b, _ := json.Marshal(req)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if m["tool_choice"] != "auto" {
		t.Fatalf("tool_choice=%v", m["tool_choice"])
	}
	if m["store"] != false {
		t.Fatalf("store=%v", m["store"])
	}
	if m["stream"] != true {
		t.Fatalf("stream=%v", m["stream"])
	}
	if m["prompt_cache_key"] != "sid" {
		t.Fatalf("prompt_cache_key=%v", m["prompt_cache_key"])
	}
	if _, ok := m["include"]; ok {
		t.Fatalf("include should be omitted when reasoning is off")
	}
	if _, ok := m["reasoning"]; ok {
		t.Fatalf("reasoning should be omitted when reasoning is off")
	}
	if tools, ok := m["tools"].([]any); !ok || tools == nil {
		t.Fatalf("tools should be an array")
	}
}

func TestBuildCodexResponsesRequest_GeneratesSessionIDWhenBlank(t *testing.T) {
	req, err := BuildCodexResponsesRequest("   ", CodexResponsesRequest{
		Model:             "gpt-4.1",
		Instructions:      "Respond with 'pong' to the input.",
		Input:             []any{map[string]any{"type": "input_text", "text": "hi"}},
		Tools:             nil,
		ToolChoice:        nil,
		ParallelToolCalls: false,
		Store:             true,
		Stream:            false,
		PromptCacheKey:    "wrong",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.PromptCacheKey == "" {
		t.Fatalf("prompt_cache_key should be set")
	}
	if _, err := uuid.Parse(req.PromptCacheKey); err != nil {
		t.Fatalf("prompt_cache_key should be a UUID: %q", req.PromptCacheKey)
	}
	if req.Store != false {
		t.Fatalf("store=%v", req.Store)
	}
	if req.Stream != true {
		t.Fatalf("stream=%v", req.Stream)
	}
}

func TestBuildCodexResponsesRequest_ReasoningEnabled(t *testing.T) {
	summary := "short"
	req, err := BuildCodexResponsesRequest("sid", CodexResponsesRequest{
		Model:             "gpt-4.1",
		Instructions:      "Respond with 'pong' to the input.",
		Input:             []any{},
		Tools:             []any{},
		ToolChoice:        "auto",
		ParallelToolCalls: true,
		Reasoning: &ReasoningParam{
			Effort:  "medium",
			Summary: summary,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b, _ := json.Marshal(req)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	inc, ok := m["include"].([]any)
	if !ok || len(inc) != 1 || inc[0] != reasoningIncludeEncrypted {
		t.Fatalf("include=%v", m["include"])
	}
	if _, ok := m["reasoning"].(map[string]any); !ok {
		t.Fatalf("reasoning=%T", m["reasoning"])
	}
}

func TestNormalizeToolChoice_InvalidFallsBackToAuto(t *testing.T) {
	req, err := BuildCodexResponsesRequest("sid", CodexResponsesRequest{
		Model:             "gpt-4.1",
		Instructions:      "Respond with 'pong' to the input.",
		Input:             []any{},
		Tools:             []any{},
		ToolChoice:        123,
		ParallelToolCalls: false,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.ToolChoice != "auto" {
		t.Fatalf("tool_choice=%v", req.ToolChoice)
	}
}

func TestBuildCodexResponsesRequest_NormalizesLegacyInputTextToMessage(t *testing.T) {
	req, err := BuildCodexResponsesRequest("sid", CodexResponsesRequest{
		Model:             "gpt-4.1",
		Instructions:      "Reply briefly.",
		Input:             []any{map[string]any{"type": "input_text", "text": "hi"}},
		Tools:             []any{},
		ToolChoice:        "auto",
		ParallelToolCalls: false,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(req.Input) != 1 {
		t.Fatalf("input len=%d", len(req.Input))
	}
	msg, ok := req.Input[0].(map[string]any)
	if !ok {
		t.Fatalf("input[0] type=%T", req.Input[0])
	}
	if msg["type"] != "message" {
		t.Fatalf("input[0].type=%v", msg["type"])
	}
	if msg["role"] != "user" {
		t.Fatalf("input[0].role=%v", msg["role"])
	}
	content, ok := msg["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("input[0].content=%T %#v", msg["content"], msg["content"])
	}
	part, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("content[0] type=%T", content[0])
	}
	if part["type"] != "input_text" || part["text"] != "hi" {
		t.Fatalf("content[0]=%#v", part)
	}
}
