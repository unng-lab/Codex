package remote

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

const reasoningIncludeEncrypted = "reasoning.encrypted_content"

// ReasoningParam enables reasoning when non-nil.
// At minimum, Effort should be set.
type ReasoningParam struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary,omitempty"`
}

// CodexResponsesRequest is the JSON body for POST /backend-api/codex/responses.
// This is a minimal schema needed by ChatMock.
//
// Required fields are enforced/normalized by BuildCodexResponsesRequest.
type CodexResponsesRequest struct {
	Model             string          `json:"model"`
	Instructions      string          `json:"instructions"`
	Input             []any           `json:"input"`
	Tools             []any           `json:"tools"`
	ToolChoice        any             `json:"tool_choice"`
	ParallelToolCalls bool            `json:"parallel_tool_calls"`
	Store             bool            `json:"store"`
	Stream            bool            `json:"stream"`
	PromptCacheKey    string          `json:"prompt_cache_key"`
	Reasoning         *ReasoningParam `json:"reasoning,omitempty"`
	Include           []string        `json:"include,omitempty"`
}

// BuildCodexResponsesRequest builds and normalizes the request body according to the constraints:
// - tool_choice defaults to "auto" if missing/invalid
// - store is always false
// - stream is always true
// - prompt_cache_key equals sessionID (or a newly-generated UUID if sessionID is blank)
// - legacy input items like {"type":"input_text","text":"..."} are wrapped into a user message
// - if reasoning != nil then include=["reasoning.encrypted_content"], else omit both reasoning/include
func BuildCodexResponsesRequest(sessionID string, req CodexResponsesRequest) (CodexResponsesRequest, error) {
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		sid = uuid.NewString()
	}
	if strings.TrimSpace(req.Model) == "" {
		return CodexResponsesRequest{}, fmt.Errorf("model is required")
	}
	if req.Input == nil {
		return CodexResponsesRequest{}, fmt.Errorf("input is required")
	}
	if req.Tools == nil {
		req.Tools = []any{}
	}

	req.ToolChoice = normalizeToolChoice(req.ToolChoice)
	req.Input = normalizeInput(req.Input)
	req.Store = false
	req.Stream = true
	req.PromptCacheKey = sid

	if req.Reasoning != nil {
		req.Include = []string{reasoningIncludeEncrypted}
	} else {
		req.Include = nil
	}

	return req, nil
}

func normalizeToolChoice(v any) any {
	switch x := v.(type) {
	case nil:
		return "auto"
	case string:
		if strings.TrimSpace(x) == "" {
			return "auto"
		}
		return x
	case map[string]any:
		if len(x) == 0 {
			return "auto"
		}
		return x
	case json.RawMessage:
		b := bytes.TrimSpace([]byte(x))
		if len(b) == 0 {
			return "auto"
		}
		// Accept either a JSON string ("auto") or an object ({...}).
		if b[0] == '"' || b[0] == '{' {
			return x
		}
		return "auto"
	default:
		// Structs/slices/etc are likely invalid for this field; fall back to auto.
		return "auto"
	}
}

func normalizeInput(in []any) []any {
	out := make([]any, 0, len(in))
	for _, item := range in {
		m, ok := item.(map[string]any)
		if !ok {
			out = append(out, item)
			continue
		}

		typ, _ := m["type"].(string)
		switch typ {
		case "input_text", "input_image", "input_file", "input_audio":
			// Backend expects top-level input items to be "message" (or other call types),
			// while input_* items belong under message.content.
			out = append(out, map[string]any{
				"type":    "message",
				"role":    "user",
				"content": []any{m},
			})
		case "":
			// If a role is provided but type is omitted, normalize to a message item.
			if _, hasRole := m["role"]; hasRole {
				cp := make(map[string]any, len(m)+1)
				for k, v := range m {
					cp[k] = v
				}
				cp["type"] = "message"
				out = append(out, cp)
				continue
			}
			out = append(out, item)
		default:
			out = append(out, item)
		}
	}
	return out
}
