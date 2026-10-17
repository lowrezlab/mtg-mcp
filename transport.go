package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/server"
)

// Transport selection and HTTP settings, all read from the environment so the
// same binary runs as a local stdio server or a hosted Streamable HTTP server.
const (
	envTransport     = "MCP_TRANSPORT"       // "stdio" (default) or "http"
	envPort          = "PORT"                // HTTP listen port (default 8080)
	envPathSecret    = "MCP_PATH_SECRET"     //nolint:gosec // env var name, not a credential
	envAllowNoSecret = "MCP_ALLOW_NO_SECRET" //nolint:gosec // env var name; "1" serves an open /mcp (local only)
	envRateLimit     = "MCP_RATE_LIMIT"      // sustained requests per second (default 5)
	envRateBurst     = "MCP_RATE_BURST"      // burst size (default 20)

	transportStdio = "stdio"
	transportHTTP  = "http"

	defaultHTTPPort    = "8080"
	mcpEndpointBase    = "/mcp"
	healthPath         = "/healthz"
	minPathSecretLen   = 24
	defaultRatePerSec  = 5.0
	defaultRateBurst   = 20
	sessionIdleTTL     = 30 * time.Minute
	heartbeatInterval  = 30 * time.Second
	readHeaderTimeout  = 10 * time.Second
	shutdownTimeout    = 10 * time.Second
	retryAfterSeconds  = "1"
	redactedEndpoint   = mcpEndpointBase + "/<secret>"
	allowNoSecretValue = "1"
)

var errMissingPathSecret = errors.New(
	envPathSecret + " is required in http mode (set " + envAllowNoSecret + "=1 to serve an open /mcp for local testing)",
)

// httpConfig is the resolved configuration for the Streamable HTTP transport.
type httpConfig struct {
	addr      string
	endpoint  string
	ratePerS  float64
	rateBurst int
}

// logEndpoint is the endpoint as it may appear in logs, with the secret redacted.
func (c httpConfig) logEndpoint() string {
	if c.endpoint == mcpEndpointBase {
		return c.endpoint
	}

	return redactedEndpoint
}

// loadHTTPConfig reads the HTTP transport configuration via getenv.
func loadHTTPConfig(getenv func(string) string) (httpConfig, error) {
	port := getenv(envPort)
	if port == "" {
		port = defaultHTTPPort
	}

	cfg := httpConfig{
		addr:      ":" + port,
		endpoint:  mcpEndpointBase,
		ratePerS:  defaultRatePerSec,
		rateBurst: defaultRateBurst,
	}

	secret := getenv(envPathSecret)
	switch {
	case secret != "":
		if err := validatePathSecret(secret); err != nil {
			return httpConfig{}, err
		}
		cfg.endpoint = mcpEndpointBase + "/" + secret
	case getenv(envAllowNoSecret) != allowNoSecretValue:
		return httpConfig{}, errMissingPathSecret
	}

	if v := getenv(envRateLimit); v != "" {
		rate, err := strconv.ParseFloat(v, 64)
		if err != nil || rate <= 0 {
			return httpConfig{}, fmt.Errorf("%s must be a positive number, got %q", envRateLimit, v)
		}
		cfg.ratePerS = rate
	}
	if v := getenv(envRateBurst); v != "" {
		burst, err := strconv.Atoi(v)
		if err != nil || burst <= 0 {
			return httpConfig{}, fmt.Errorf("%s must be a positive integer, got %q", envRateBurst, v)
		}
		cfg.rateBurst = burst
	}

	return cfg, nil
}

// validatePathSecret requires a long, URL-safe secret so the endpoint path
// cannot be guessed and needs no escaping.
func validatePathSecret(secret string) error {
	if len(secret) < minPathSecretLen {
		return fmt.Errorf("%s must be at least %d characters", envPathSecret, minPathSecretLen)
	}
	for _, r := range secret {
		isAlnum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !isAlnum && r != '-' && r != '_' {
			return fmt.Errorf("%s may only contain letters, digits, '-' and '_'", envPathSecret)
		}
	}

	return nil
}

// tokenBucket is a minimal concurrency-safe token-bucket rate limiter.
type tokenBucket struct {
	mu       sync.Mutex
	tokens   float64
	capacity float64
	rate     float64 // tokens added per second
	last     time.Time
	now      func() time.Time
}

func newTokenBucket(ratePerSec float64, burst int, now func() time.Time) *tokenBucket {
	return &tokenBucket{
		tokens:   float64(burst),
		capacity: float64(burst),
		rate:     ratePerSec,
		last:     now(),
		now:      now,
	}
}

// allow reports whether a request may proceed, consuming a token if so.
func (b *tokenBucket) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	b.tokens = min(b.capacity, b.tokens+now.Sub(b.last).Seconds()*b.rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--

	return true
}

// rateLimit rejects requests with 429 once the bucket is empty. The server is
// single-user, so one global bucket caps how hard a runaway client can drive
// the upstream APIs (and get this host's IP blocked by them).
func rateLimit(bucket *tokenBucket, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !bucket.allow() {
			w.Header().Set("Retry-After", retryAfterSeconds)
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)

			return
		}
		next.ServeHTTP(w, r)
	})
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", mimeTypeTextPlain)
	_, _ = w.Write([]byte("ok\n"))
}

// newHTTPHandler mounts the MCP endpoint (rate limited) and the health check.
// Every other path, including a bare /mcp when a secret is set, returns 404.
func newHTTPHandler(streamable http.Handler, cfg httpConfig) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+healthPath, handleHealth)
	mux.Handle(cfg.endpoint, rateLimit(newTokenBucket(cfg.ratePerS, cfg.rateBurst, time.Now), streamable))

	return mux
}

// serveHTTP runs the MCP server over Streamable HTTP until SIGINT/SIGTERM.
// Sessions are stateful (the get_card_image widget reads a resource on the same
// session after the tool call), so run a single instance or use sticky sessions.
func serveHTTP(mcpServer *server.MCPServer, cfg httpConfig) error {
	log := GetLogger()

	streamable := server.NewStreamableHTTPServer(mcpServer,
		server.WithStateful(true),
		server.WithHeartbeatInterval(heartbeatInterval),
		server.WithSessionIdleTTL(sessionIdleTTL),
	)
	httpServer := &http.Server{
		Addr:              cfg.addr,
		Handler:           newHTTPHandler(streamable, cfg),
		ReadHeaderTimeout: readHeaderTimeout,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Info().
			Str("transport", transportHTTP).
			Str("addr", cfg.addr).
			Str("endpoint", cfg.logEndpoint()).
			Float64("rate_per_sec", cfg.ratePerS).
			Int("rate_burst", cfg.rateBurst).
			Msg("Starting MTG Commander MCP Server")
		errCh <- httpServer.ListenAndServe()
	}()

	var listenErr error
	select {
	case err := <-errCh:
		listenErr = fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		log.Info().Msg("Shutting down HTTP server")
	}

	// Always stop the session sweeper and close sessions, even if listening failed.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	_ = streamable.Shutdown(shutdownCtx)
	if listenErr != nil {
		return listenErr
	}
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}

	return nil
}

// serve starts the transport selected by MCP_TRANSPORT (stdio by default).
func serve(mcpServer *server.MCPServer, logFilePath string) error {
	log := GetLogger()

	switch transport := os.Getenv(envTransport); transport {
	case "", transportStdio:
		log.Info().
			Str("transport", transportStdio).
			Str("log_file", logFilePath).
			Msg("Starting MTG Commander MCP Server")

		if err := server.ServeStdio(mcpServer); err != nil {
			return fmt.Errorf("stdio server: %w", err)
		}

		return nil
	case transportHTTP:
		cfg, err := loadHTTPConfig(os.Getenv)
		if err != nil {
			return err
		}

		return serveHTTP(mcpServer, cfg)
	default:
		return fmt.Errorf("unknown %s %q (want %q or %q)", envTransport, transport, transportStdio, transportHTTP)
	}
}
