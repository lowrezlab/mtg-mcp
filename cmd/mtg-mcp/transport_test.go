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
	"github.com/nathanmartins/mtg-mcp/internal/mtgserver"
)

const testPathSecret = "abcdefghijklmnopqrstuvwxyz_0123-AB"

type rpcResponse struct {
	statusCode int
	header     http.Header
}

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadHTTPConfig(t *testing.T) {
	t.Parallel()

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
			wantEndpoint: mcpEndpointBase,
			wantRate:     defaultRatePerSec,
			wantBurst:    defaultRateBurst,
		},
		{
			name:         "secret path and port",
			env:          map[string]string{envPathSecret: testPathSecret, envPort: "9090"},
			wantAddr:     ":9090",
			wantEndpoint: mcpEndpointBase + "/" + testPathSecret,
			wantRate:     defaultRatePerSec,
			wantBurst:    defaultRateBurst,
		},
		{
			name:         "rate limit overrides",
			env:          map[string]string{envPathSecret: testPathSecret, envRateLimit: "2.5", envRateBurst: "7"},
			wantAddr:     ":8080",
			wantEndpoint: mcpEndpointBase + "/" + testPathSecret,
			wantRate:     2.5,
			wantBurst:    7,
		},
		{
			name:       "secret too short",
			env:        map[string]string{envPathSecret: "short"},
			wantErr:    errInvalidPathSecret,
			wantErrSub: "at least",
		},
		{
			name:       "secret with unsafe characters",
			env:        map[string]string{envPathSecret: "abcdefghijklmnopqrstuvwxyz/../x"},
			wantErr:    errInvalidPathSecret,
			wantErrSub: "may only contain",
		},
		{
			name:       "invalid rate",
			env:        map[string]string{envPathSecret: testPathSecret, envRateLimit: "0"},
			wantErr:    errInvalidRateLimit,
			wantErrSub: envRateLimit,
		},
		{
			name:    "not a number rate",
			env:     map[string]string{envPathSecret: testPathSecret, envRateLimit: "NaN"},
			wantErr: errInvalidRateLimit,
		},
		{
			name:    "positive infinite rate",
			env:     map[string]string{envPathSecret: testPathSecret, envRateLimit: "+Inf"},
			wantErr: errInvalidRateLimit,
		},
		{
			name:    "negative infinite rate",
			env:     map[string]string{envPathSecret: testPathSecret, envRateLimit: "-Inf"},
			wantErr: errInvalidRateLimit,
		},
		{
			name:       "invalid burst",
			env:        map[string]string{envPathSecret: testPathSecret, envRateBurst: "lots"},
			wantErr:    errInvalidRateBurst,
			wantErrSub: envRateBurst,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := loadHTTPConfig(envFrom(tt.env))
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
				if tt.wantErrSub != "" && !strings.Contains(err.Error(), tt.wantErrSub) {
					t.Fatalf("error = %v, want it to mention %q", err, tt.wantErrSub)
				}
				return
			}
			if err != nil {
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
	t.Parallel()

	withSecret := httpConfig{endpoint: mcpEndpointBase + "/" + testPathSecret}
	if got := withSecret.logEndpoint(); strings.Contains(got, testPathSecret) {
		t.Errorf("logEndpoint() = %q leaks the secret", got)
	}
	if got := (httpConfig{endpoint: mcpEndpointBase}).logEndpoint(); got != mcpEndpointBase {
		t.Errorf("logEndpoint() = %q, want /mcp", got)
	}
}

func TestTokenBucket(t *testing.T) {
	t.Parallel()

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

	s, err := mtgserver.New()
	if err != nil {
		t.Fatalf("mtgserver.New() error = %v", err)
	}
	mcpServer := server.NewMCPServer("test", "0.0.0", server.WithRecovery())
	s.Register(mcpServer)

	streamable := server.NewStreamableHTTPServer(mcpServer, server.WithStateful(true))
	ts := httptest.NewServer(newHTTPHandler(streamable, cfg))
	t.Cleanup(func() {
		ts.Close()
		_ = streamable.Shutdown(t.Context())
	})

	return ts
}

// postRPC sends one JSON-RPC message, consumes and closes the body, and returns
// HTTP metadata and its decoded result (from either a JSON or an SSE body).
func postRPC(t *testing.T, url, sessionID, body string) (rpcResponse, map[string]any) {
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

	return rpcResponse{statusCode: resp.StatusCode, header: resp.Header.Clone()}, msg
}

const initializeRPC = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":` +
	`{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`

func TestHTTPHandlerServesMCPOnSecretPath(t *testing.T) {
	t.Parallel()

	cfg := httpConfig{endpoint: mcpEndpointBase + "/" + testPathSecret, ratePerS: 100, rateBurst: 100}
	ts := newTestHTTPServer(t, cfg)

	resp, _ := postRPC(t, ts.URL+mcpEndpointBase, "", initializeRPC)
	if resp.statusCode != http.StatusNotFound {
		t.Errorf("bare /mcp status = %d, want 404 when a secret is set", resp.statusCode)
	}

	resp, _ = postRPC(t, ts.URL+cfg.endpoint, "", initializeRPC)
	if resp.statusCode != http.StatusOK {
		t.Fatalf("initialize status = %d, want 200", resp.statusCode)
	}
	sessionID := resp.header.Get("Mcp-Session-Id")
	if sessionID == "" {
		t.Fatal("stateful server should return an Mcp-Session-Id")
	}

	_, msg := postRPC(t, ts.URL+cfg.endpoint, sessionID, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	result, _ := msg["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 20 {
		t.Errorf("tools/list returned %d tools, want %d", len(tools), 20)
	}

	resp, msg = postRPC(t, ts.URL+cfg.endpoint, sessionID,
		`{"jsonrpc":"2.0","id":4,"method":"resources/read","params":{"uri":"ui://mtg-card"}}`)
	if resp.statusCode != http.StatusOK || msg["error"] != nil {
		t.Fatalf("same-session widget read status = %d, response = %v", resp.statusCode, msg)
	}
	resourceResult, _ := msg["result"].(map[string]any)
	contents, _ := resourceResult["contents"].([]any)
	if len(contents) != 1 {
		t.Fatalf("widget contents = %v, want one document", contents)
	}
	widget, _ := contents[0].(map[string]any)
	if widget["uri"] != "ui://mtg-card" || widget["mimeType"] != "text/html;profile=mcp-app" {
		t.Errorf("widget contents = %v, want registered MCP Apps HTML", widget)
	}
	if text, _ := widget["text"].(string); text == "" {
		t.Error("widget resource must contain HTML")
	}

	for _, path := range []string{"", "/", "/mcp/wrong-secret", mcpEndpointBase + "/" + testPathSecret + "/extra"} {
		pathResponse, _ := postRPC(t, ts.URL+path, "", initializeRPC)
		if pathResponse.statusCode != http.StatusNotFound {
			t.Errorf("path %q status = %d, want 404", path, pathResponse.statusCode)
		}
	}

	resp, _ = postRPC(t, ts.URL+cfg.endpoint, "mcp-session-unknown", `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	if resp.statusCode == http.StatusOK {
		t.Error("an unknown session ID should be rejected by the stateful server")
	}
}

func TestHTTPHandlerHealthAndRateLimit(t *testing.T) {
	t.Parallel()

	cfg := httpConfig{endpoint: mcpEndpointBase + "/" + testPathSecret, ratePerS: 0.001, rateBurst: 1}
	ts := newTestHTTPServer(t, cfg)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+healthPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	health, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if closeErr := health.Body.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if health.StatusCode != http.StatusOK {
		t.Errorf("health status = %d, want 200", health.StatusCode)
	}

	if resp, _ := postRPC(t, ts.URL+cfg.endpoint, "", initializeRPC); resp.statusCode != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", resp.statusCode)
	}
	resp, _ := postRPC(t, ts.URL+cfg.endpoint, "", initializeRPC)
	if resp.statusCode != http.StatusTooManyRequests {
		t.Errorf("second request status = %d, want 429", resp.statusCode)
	}
	if resp.header.Get("Retry-After") == "" {
		t.Error("429 response should carry Retry-After")
	}

	health, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Errorf("health status after MCP rate limit exhausted = %d, want 200", health.StatusCode)
	}
}

func TestServeRejectsBadTransportConfig(t *testing.T) {
	mcpServer := server.NewMCPServer("test", "0.0.0")

	t.Setenv(envTransport, "carrier-pigeon")
	if err := serve(mcpServer, ""); !errors.Is(err, errUnknownTransport) {
		t.Errorf("unknown transport error = %v, want %v", err, errUnknownTransport)
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
