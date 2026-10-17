package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"
)

const testPathSecret = "abcdefghijklmnopqrstuvwxyz_0123-AB"

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadHTTPConfig(t *testing.T) {
	tests := []struct {
		name         string
		env          map[string]string
		wantErr      error
		wantErrSub   string
		wantAddr     string
		wantEndpoint string
		wantRate     float64
		wantBurst    int
	}{
		{
			name:    "secret required by default",
			env:     map[string]string{},
			wantErr: errMissingPathSecret,
		},
		{
			name:         "open endpoint when explicitly allowed",
			env:          map[string]string{envAllowNoSecret: "1"},
			wantAddr:     ":8080",
			wantEndpoint: "/mcp",
			wantRate:     defaultRatePerSec,
			wantBurst:    defaultRateBurst,
		},
		{
			name:         "secret path and port",
			env:          map[string]string{envPathSecret: testPathSecret, envPort: "9090"},
			wantAddr:     ":9090",
			wantEndpoint: "/mcp/" + testPathSecret,
			wantRate:     defaultRatePerSec,
			wantBurst:    defaultRateBurst,
		},
		{
			name:         "rate limit overrides",
			env:          map[string]string{envPathSecret: testPathSecret, envRateLimit: "2.5", envRateBurst: "7"},
			wantAddr:     ":8080",
			wantEndpoint: "/mcp/" + testPathSecret,
			wantRate:     2.5,
			wantBurst:    7,
		},
		{
			name:       "secret too short",
			env:        map[string]string{envPathSecret: "short"},
			wantErrSub: "at least",
		},
		{
			name:       "secret with unsafe characters",
			env:        map[string]string{envPathSecret: "abcdefghijklmnopqrstuvwxyz/../x"},
			wantErrSub: "may only contain",
		},
		{
			name:       "invalid rate",
			env:        map[string]string{envPathSecret: testPathSecret, envRateLimit: "0"},
			wantErrSub: envRateLimit,
		},
		{
			name:       "invalid burst",
			env:        map[string]string{envPathSecret: testPathSecret, envRateBurst: "lots"},
			wantErrSub: envRateBurst,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := loadHTTPConfig(envFrom(tt.env))
			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
				return
			case tt.wantErrSub != "":
				if err == nil || !strings.Contains(err.Error(), tt.wantErrSub) {
					t.Fatalf("error = %v, want it to mention %q", err, tt.wantErrSub)
				}
				return
			case err != nil:
				t.Fatalf("unexpected error: %v", err)
			}

			if cfg.addr != tt.wantAddr || cfg.endpoint != tt.wantEndpoint {
				t.Errorf("addr/endpoint = %q %q, want %q %q", cfg.addr, cfg.endpoint, tt.wantAddr, tt.wantEndpoint)
			}
			if cfg.ratePerS != tt.wantRate || cfg.rateBurst != tt.wantBurst {
				t.Errorf("rate/burst = %v/%d, want %v/%d", cfg.ratePerS, cfg.rateBurst, tt.wantRate, tt.wantBurst)
			}
		})
	}
}

func TestHTTPConfigLogEndpointRedactsSecret(t *testing.T) {
	withSecret := httpConfig{endpoint: "/mcp/" + testPathSecret}
	if got := withSecret.logEndpoint(); strings.Contains(got, testPathSecret) {
		t.Errorf("logEndpoint() = %q leaks the secret", got)
	}
	if got := (httpConfig{endpoint: "/mcp"}).logEndpoint(); got != "/mcp" {
		t.Errorf("logEndpoint() = %q, want /mcp", got)
	}
}

func TestTokenBucket(t *testing.T) {
	now := time.Unix(0, 0)
	bucket := newTokenBucket(1, 2, func() time.Time { return now })

	for range 2 {
		if !bucket.allow() {
			t.Fatal("burst of 2 should allow two requests")
		}
	}
	if bucket.allow() {
		t.Fatal("third request should be limited")
	}

	now = now.Add(time.Second)
	if !bucket.allow() {
		t.Fatal("one token should refill after one second")
	}
	if bucket.allow() {
		t.Fatal("bucket should be empty again")
	}

	now = now.Add(time.Hour)
	for range 2 {
		if !bucket.allow() {
			t.Fatal("refill should be capped at the burst size, not lost")
		}
	}
	if bucket.allow() {
		t.Fatal("refill must not exceed the burst size")
	}
}

// newTestHTTPServer serves the real MCP tool set over the HTTP handler.
func newTestHTTPServer(t *testing.T, cfg httpConfig) *httptest.Server {
	t.Helper()

	s, err := NewMTGCommanderServer()
	if err != nil {
		t.Fatalf("NewMTGCommanderServer() error = %v", err)
	}
	mcpServer := server.NewMCPServer("test", "0.0.0", server.WithRecovery())
	s.registerTools(mcpServer)
	s.registerResources(mcpServer)

	streamable := server.NewStreamableHTTPServer(mcpServer, server.WithStateful(true))
	ts := httptest.NewServer(newHTTPHandler(streamable, cfg))
	t.Cleanup(ts.Close)

	return ts
}

// postRPC sends one JSON-RPC message and returns the response and its decoded
// result (from either a JSON or an SSE response body).
func postRPC(t *testing.T, url, sessionID, body string) (*http.Response, map[string]any) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		scanner := bufio.NewScanner(bytes.NewReader(raw))
		for scanner.Scan() {
			if data, ok := strings.CutPrefix(scanner.Text(), "data:"); ok {
				raw = []byte(data)
				break
			}
		}
	}

	var msg map[string]any
	_ = json.Unmarshal(raw, &msg)

	return resp, msg
}

const initializeRPC = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":` +
	`{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`

func TestHTTPHandlerServesMCPOnSecretPath(t *testing.T) {
	cfg := httpConfig{endpoint: "/mcp/" + testPathSecret, ratePerS: 100, rateBurst: 100}
	ts := newTestHTTPServer(t, cfg)

	resp, _ := postRPC(t, ts.URL+"/mcp", "", initializeRPC)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("bare /mcp status = %d, want 404 when a secret is set", resp.StatusCode)
	}

	resp, _ = postRPC(t, ts.URL+cfg.endpoint, "", initializeRPC)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("initialize status = %d, want 200", resp.StatusCode)
	}
	sessionID := resp.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		t.Fatal("stateful server should return an Mcp-Session-Id")
	}

	_, msg := postRPC(t, ts.URL+cfg.endpoint, sessionID, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	result, _ := msg["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != totalToolCount {
		t.Errorf("tools/list returned %d tools, want %d", len(tools), totalToolCount)
	}

	resp, _ = postRPC(t, ts.URL+cfg.endpoint, "mcp-session-unknown", `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	if resp.StatusCode == http.StatusOK {
		t.Error("an unknown session ID should be rejected by the stateful server")
	}
}

func TestHTTPHandlerHealthAndRateLimit(t *testing.T) {
	cfg := httpConfig{endpoint: "/mcp/" + testPathSecret, ratePerS: 0.001, rateBurst: 1}
	ts := newTestHTTPServer(t, cfg)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+healthPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	health, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Errorf("health status = %d, want 200", health.StatusCode)
	}

	if resp, _ := postRPC(t, ts.URL+cfg.endpoint, "", initializeRPC); resp.StatusCode != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", resp.StatusCode)
	}
	resp, _ := postRPC(t, ts.URL+cfg.endpoint, "", initializeRPC)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("second request status = %d, want 429", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("429 response should carry Retry-After")
	}
}

func TestServeRejectsBadTransportConfig(t *testing.T) {
	mcpServer := server.NewMCPServer("test", "0.0.0")

	t.Setenv(envTransport, "carrier-pigeon")
	if err := serve(mcpServer, ""); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Errorf("unknown transport error = %v", err)
	}

	t.Setenv(envTransport, transportHTTP)
	t.Setenv(envPathSecret, "")
	t.Setenv(envAllowNoSecret, "")
	if err := serve(mcpServer, ""); !errors.Is(err, errMissingPathSecret) {
		t.Errorf("http without a secret error = %v, want %v", err, errMissingPathSecret)
	}

	t.Setenv(envPathSecret, testPathSecret)
	t.Setenv(envPort, "not-a-port")
	if err := serve(mcpServer, ""); err == nil || !strings.Contains(err.Error(), "http server") {
		t.Errorf("bad port error = %v, want a listen failure", err)
	}
}
