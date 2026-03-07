package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"brainhub/internal/buildinfo"
)

func TestServer_ResponsesProxyStream(t *testing.T) {
	var got struct {
		Auth      string
		AccountID string
		SessionID string
		Body      map[string]any
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method=%s", r.Method)
		}
		if r.URL.Path != "/backend-api/codex/responses" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		got.Auth = r.Header.Get("Authorization")
		got.AccountID = r.Header.Get("chatgpt-account-id")
		got.SessionID = r.Header.Get("session_id")
		defer r.Body.Close()
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(b, &got.Body); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"ok\":true}\n\n"))
	}))
	defer upstream.Close()

	srv, err := NewServer(Config{
		AccessToken: "tok",
		AccountID:   "acc",
		BaseURL:     upstream.URL,
		HTTPClient:  upstream.Client(),
	}, nil)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	reqBody := []byte(`{"model":"gpt-5","input":"ping","stream":true}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(headerSessionID, "sid-123")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)
	res := rr.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type=%q", ct)
	}
	body, _ := io.ReadAll(res.Body)
	if string(body) != "data: {\"ok\":true}\n\n" {
		t.Fatalf("body=%q", string(body))
	}

	if got.Auth != "Bearer tok" {
		t.Fatalf("auth=%q", got.Auth)
	}
	if got.AccountID != "acc" {
		t.Fatalf("account=%q", got.AccountID)
	}
	if got.SessionID != "sid-123" {
		t.Fatalf("session_id=%q", got.SessionID)
	}
	if got.Body["model"] != "gpt-5" {
		t.Fatalf("model=%v", got.Body["model"])
	}
	if got.Body["prompt_cache_key"] != "sid-123" {
		t.Fatalf("prompt_cache_key=%v", got.Body["prompt_cache_key"])
	}
	if got.Body["stream"] != true {
		t.Fatalf("stream=%v", got.Body["stream"])
	}
}

func TestServer_ResponsesRejectsStreamFalse(t *testing.T) {
	srv, err := NewServer(Config{
		AccessToken: "tok",
		AccountID:   "acc",
		BaseURL:     "https://example.com",
	}, nil)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"x","stream":false}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)
	res := rr.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", res.StatusCode)
	}
	var doc map[string]any
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	errObj, ok := doc["error"].(map[string]any)
	if !ok {
		t.Fatalf("missing error object: %#v", doc)
	}
	msg, _ := errObj["message"].(string)
	if !strings.Contains(msg, "stream=true") {
		t.Fatalf("message=%q", msg)
	}
}

func TestServer_Healthz(t *testing.T) {
	buildinfo.Version = "v1.2.3"
	buildinfo.Commit = "abc123"
	buildinfo.BuildDate = "2026-03-06T00:00:00Z"
	buildinfo.Channel = "stable"

	srv, err := NewServer(Config{AccessToken: "tok", AccountID: "acc"}, nil)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	var doc map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if ok, _ := doc["ok"].(bool); !ok {
		t.Fatalf("ok=%v", doc["ok"])
	}
	if got, _ := doc["version"].(string); got != "v1.2.3" {
		t.Fatalf("version=%q", got)
	}
	if got, _ := doc["channel"].(string); got != "stable" {
		t.Fatalf("channel=%q", got)
	}
	if got, _ := doc["service"].(string); got != "brainhub-proxy" {
		t.Fatalf("service=%q", got)
	}
}

func TestServer_Versionz(t *testing.T) {
	buildinfo.Version = "v9.9.9"
	buildinfo.Commit = "def456"
	buildinfo.BuildDate = "2026-03-07T00:00:00Z"
	buildinfo.Channel = "beta"

	srv, err := NewServer(Config{AccessToken: "tok", AccountID: "acc", ServiceName: "brainhub-tray"}, nil)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/versionz", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}

	var doc map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if got, _ := doc["version"].(string); got != "v9.9.9" {
		t.Fatalf("version=%q", got)
	}
	if got, _ := doc["commit"].(string); got != "def456" {
		t.Fatalf("commit=%q", got)
	}
	if got, _ := doc["service"].(string); got != "brainhub-tray" {
		t.Fatalf("service=%q", got)
	}
}
