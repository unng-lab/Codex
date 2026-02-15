package app

import (
	"log"
	"net/http"
	"os"
	"strings"

	"chatmock/internal/api"
	"chatmock/internal/remote"
	"chatmock/internal/rules"
)

type Server struct {
	handlers *api.Handlers
}

func NewServer() *Server {
	store := rules.NewStore([]rules.Rule{
		{Contains: "hello", Reply: "Hi! This is a mocked assistant response."},
		{Contains: "weather", Reply: "The mock forecast: sunny with a chance of unit tests."},
	})
	manager := remote.NewManager(initialProvidersFromEnv())
	return &Server{handlers: api.NewHandlers(store, manager)}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handlers.Health)
	mux.HandleFunc("/v1/chat/completions", s.handlers.ChatCompletions)
	mux.HandleFunc("/v1/completions", s.handlers.Completions)
	mux.HandleFunc("/v1/responses", s.handlers.Responses)
	mux.HandleFunc("/v1/models", s.handlers.Models)
	mux.HandleFunc("/api/chat", s.handlers.OllamaChat)
	mux.HandleFunc("/api/tags", s.handlers.OllamaTags)
	mux.HandleFunc("/api/show", s.handlers.OllamaShow)
	mux.HandleFunc("/api/version", s.handlers.OllamaVersion)
	mux.HandleFunc("/v1/rules", s.handlers.Rules)
	mux.HandleFunc("/v1/providers", s.handlers.Providers)
	return loggingMiddleware(mux)
}

func (s *Server) ListenAndServe() error {
	addr := env("CHATMOCK_ADDR", ":8080")
	log.Printf("ChatMock (Go) listening on %s", addr)
	return http.ListenAndServe(addr, s.Routes())
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

func initialProvidersFromEnv() []remote.Provider {
	providers := make([]remote.Provider, 0, 3)

	if baseURL := strings.TrimSpace(os.Getenv("CHATMOCK_OLLAMA_BASE_URL")); baseURL != "" {
		providers = append(providers, remote.Provider{
			Name:        "ollama",
			Kind:        "ollama",
			BaseURL:     baseURL,
			ModelPrefix: env("CHATMOCK_OLLAMA_MODEL_PREFIX", "ollama/"),
			RouteAll:    envBool("CHATMOCK_OLLAMA_ROUTE_ALL", false),
		})
	}

	if baseURL := strings.TrimSpace(os.Getenv("CHATMOCK_CODEX_BASE_URL")); baseURL != "" {
		providers = append(providers, remote.Provider{
			Name:        "codex",
			Kind:        "codex",
			BaseURL:     baseURL,
			APIKey:      os.Getenv("CHATMOCK_CODEX_API_KEY"),
			ModelPrefix: env("CHATMOCK_CODEX_MODEL_PREFIX", "codex/"),
			RouteAll:    envBool("CHATMOCK_CODEX_ROUTE_ALL", false),
		})
	}

	if baseURL := strings.TrimSpace(os.Getenv("CHATMOCK_CHATGPT_BASE_URL")); baseURL != "" || strings.TrimSpace(os.Getenv("CHATMOCK_CHATGPT_ACCESS_TOKEN")) != "" {
		if strings.TrimSpace(baseURL) == "" {
			baseURL = "https://chatgpt.com"
		}
		providers = append(providers, remote.Provider{
			Name:        "chatgpt",
			Kind:        "chatgpt",
			BaseURL:     baseURL,
			AccessToken: os.Getenv("CHATMOCK_CHATGPT_ACCESS_TOKEN"),
			AccountID:   os.Getenv("CHATMOCK_CHATGPT_ACCOUNT_ID"),
			ModelPrefix: env("CHATMOCK_CHATGPT_MODEL_PREFIX", "chatgpt/"),
			RouteAll:    envBool("CHATMOCK_CHATGPT_ROUTE_ALL", false),
		})
	}

	// Backward compatibility for previous single remote provider variables.
	if baseURL := strings.TrimSpace(os.Getenv("CHATMOCK_REMOTE_BASE_URL")); baseURL != "" {
		providers = append(providers, remote.Provider{
			Name:        "remote",
			Kind:        "openai",
			BaseURL:     baseURL,
			APIKey:      os.Getenv("CHATMOCK_REMOTE_API_KEY"),
			ModelPrefix: env("CHATMOCK_REMOTE_MODEL_PREFIX", "remote/"),
			RouteAll:    envBool("CHATMOCK_REMOTE_ROUTE_ALL", false),
		})
	}

	return providers
}

func env(key, fallback string) string {
	if v := os.Getenv(key); strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
