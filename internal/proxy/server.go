package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"brainhub/internal/buildinfo"
	"brainhub/internal/remote"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	headerSessionID = "X-Session-ID"
)

type Config struct {
	AccessToken string
	AccountID   string
	BaseURL     string
	HTTPClient  *http.Client
	ServiceName string
}

type Server struct {
	accessToken string
	accountID   string
	client      *remote.ChatGPTCodexClient
	mux         *http.ServeMux
	logger      *zap.Logger
	serviceName string
}

type statusResponse struct {
	OK        bool   `json:"ok"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	Channel   string `json:"channel"`
	Service   string `json:"service"`
}

func NewServer(cfg Config, logger *zap.Logger) (*Server, error) {
	if strings.TrimSpace(cfg.AccessToken) == "" {
		return nil, fmt.Errorf("access token is required")
	}
	if strings.TrimSpace(cfg.AccountID) == "" {
		return nil, fmt.Errorf("account id is required")
	}

	s := &Server{
		accessToken: strings.TrimSpace(cfg.AccessToken),
		accountID:   strings.TrimSpace(cfg.AccountID),
		client:      remote.NewChatGPTCodexClient(cfg.BaseURL, cfg.HTTPClient),
		mux:         http.NewServeMux(),
		logger:      logger,
		serviceName: strings.TrimSpace(cfg.ServiceName),
	}
	if s.logger == nil {
		l, err := zap.NewDevelopment()
		if err != nil {
			return nil, fmt.Errorf("create default logger: %w", err)
		}
		s.logger = l
	}
	if s.serviceName == "" {
		s.serviceName = "brainhub-proxy"
	}
	s.logger = s.logger.With(zap.String("service", s.serviceName))

	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.HandleFunc("/versionz", s.handleVersionz)
	s.mux.HandleFunc("/v1/responses", s.handleResponses)
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, s.statusResponse())
}

func (s *Server) handleVersionz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, s.statusResponse())
}

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	payload, streamEnabled, err := parseOpenAIResponsesRequest(r.Body)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !streamEnabled {
		writeOpenAIError(w, http.StatusBadRequest, "only stream=true is supported")
		return
	}

	sessionID := strings.TrimSpace(r.Header.Get(headerSessionID))
	if sessionID == "" {
		sessionID = uuid.NewString()
	}

	body, err := remote.BuildCodexResponsesRequest(sessionID, payload)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if raw, err := json.Marshal(body); err == nil {
		s.logger.Debug("codex request",
			zap.String("session_id", sessionID),
			zap.String("account_id", maskValue(s.accountID)),
			zap.ByteString("body", raw),
		)
	}

	resp, err := s.client.DoCodexResponses(r.Context(), s.accessToken, s.accountID, sessionID, body)
	if err != nil {
		var httpErr *remote.HTTPError
		if errors.As(err, &httpErr) {
			s.logger.Error("codex response error",
				zap.String("session_id", sessionID),
				zap.Int("status", httpErr.StatusCode),
				zap.ByteString("body", httpErr.Body),
			)
			msg := strings.TrimSpace(string(httpErr.Body))
			if msg == "" {
				msg = fmt.Sprintf("upstream http %d", httpErr.StatusCode)
			}
			writeOpenAIError(w, httpErr.StatusCode, msg)
			return
		}
		s.logger.Error("codex proxy error",
			zap.String("session_id", sessionID),
			zap.Error(err),
		)
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	s.logger.Debug("codex response",
		zap.String("session_id", sessionID),
		zap.Int("status", resp.StatusCode),
		zap.String("content_type", resp.Header.Get("Content-Type")),
	)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	err = remote.ReadSSELines(r.Context(), resp.Body, func(line string) error {
		s.logger.Debug("codex sse",
			zap.String("session_id", sessionID),
			zap.String("line", line),
		)
		if _, writeErr := io.WriteString(w, line+"\n"); writeErr != nil {
			return writeErr
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	})
	if err != nil && !errors.Is(err, r.Context().Err()) {
		s.logger.Error("codex stream error",
			zap.String("session_id", sessionID),
			zap.Error(err),
		)
	}
}

func parseOpenAIResponsesRequest(r io.Reader) (remote.CodexResponsesRequest, bool, error) {
	dec := json.NewDecoder(r)
	dec.UseNumber()

	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return remote.CodexResponsesRequest{}, false, fmt.Errorf("invalid json body: %w", err)
	}

	model, _ := raw["model"].(string)
	model = strings.TrimSpace(model)
	if model == "" {
		return remote.CodexResponsesRequest{}, false, fmt.Errorf("model is required")
	}

	input, err := normalizeInput(raw["input"])
	if err != nil {
		return remote.CodexResponsesRequest{}, false, err
	}

	instructions, _ := raw["instructions"].(string)
	tools, _ := raw["tools"].([]any)
	toolChoice := raw["tool_choice"]
	parallelToolCalls, _ := raw["parallel_tool_calls"].(bool)
	reasoning, err := parseReasoning(raw["reasoning"])
	if err != nil {
		return remote.CodexResponsesRequest{}, false, err
	}

	streamEnabled := true
	if stream, ok := raw["stream"].(bool); ok {
		streamEnabled = stream
	}

	return remote.CodexResponsesRequest{
		Model:             model,
		Instructions:      strings.TrimSpace(instructions),
		Input:             input,
		Tools:             tools,
		ToolChoice:        toolChoice,
		ParallelToolCalls: parallelToolCalls,
		Reasoning:         reasoning,
	}, streamEnabled, nil
}

func normalizeInput(v any) ([]any, error) {
	switch x := v.(type) {
	case nil:
		return nil, fmt.Errorf("input is required")
	case string:
		if strings.TrimSpace(x) == "" {
			return nil, fmt.Errorf("input must not be empty")
		}
		return []any{map[string]any{"type": "input_text", "text": x}}, nil
	case []any:
		return x, nil
	case map[string]any:
		return []any{x}, nil
	default:
		return nil, fmt.Errorf("input must be a string, object, or array")
	}
}

func parseReasoning(v any) (*remote.ReasoningParam, error) {
	if v == nil {
		return nil, nil
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("reasoning must be an object")
	}

	effort, _ := obj["effort"].(string)
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return nil, fmt.Errorf("reasoning.effort is required")
	}

	summary, _ := obj["summary"].(string)
	return &remote.ReasoningParam{
		Effort:  effort,
		Summary: strings.TrimSpace(summary),
	}, nil
}

func (s *Server) statusResponse() statusResponse {
	return statusResponse{
		OK:        true,
		Version:   buildinfo.ValueOrUnknown(buildinfo.Version),
		Commit:    buildinfo.ValueOrUnknown(buildinfo.Commit),
		BuildDate: buildinfo.ValueOrUnknown(buildinfo.BuildDate),
		Channel:   buildinfo.ValueOrUnknown(buildinfo.Channel),
		Service:   s.serviceName,
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	}
}

func writeOpenAIError(w http.ResponseWriter, status int, message string) {
	if status < 400 {
		status = http.StatusBadRequest
	}
	if strings.TrimSpace(message) == "" {
		message = "request failed"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "invalid_request_error",
		},
	})
}

func maskValue(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if len(v) <= 6 {
		return "***"
	}
	return v[:3] + "***" + v[len(v)-3:]
}
