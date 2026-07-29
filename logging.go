package middlewares

import (
	"bufio"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

// LoggingConfig controls the behavior of the Logger middleware.
type LoggingConfig struct {
	// Verbose enables per-request logging even in dev mode.
	// May also be enabled via the GOTHIC_VERBOSE env var.
	Verbose bool
}

// responseWriter wraps http.ResponseWriter to capture the status code
// written by the downstream handler.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if rw.statusCode == 0 {
		rw.statusCode = http.StatusOK
	}
	return rw.ResponseWriter.Write(b)
}

// Unwrap returns the underlying http.ResponseWriter for use by
// http.NewResponseController (Go 1.20+).
func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

// Flush sends any buffered data to the client (required for SSE, streaming).
func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack lets the caller take over the connection (required for WebSocket upgrades).
func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := rw.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// Push initiates HTTP/2 server push.
func (rw *responseWriter) Push(target string, opts *http.PushOptions) error {
	if p, ok := rw.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}

var rwPool = sync.Pool{
	New: func() any {
		return new(responseWriter)
	},
}

// Logger returns a chi-compatible HTTP middleware that logs every request.
//
// The config argument is optional: Logger() takes every setting from the
// environment, and Logger(LoggingConfig{Verbose: true}) pins verbosity in code.
//
// In dev mode (GOTHIC_MODE=dev) with no verbose flag the middleware is a
// no-op passthrough. Log format is selected by GOTHIC_PROVIDER: "AWS"
// produces JSON for a log collector. Otherwise the output is a coloured
// request line on an interactive terminal, and plain key=value text whenever
// the stream is redirected. The LoggingConfig.Verbose field and GOTHIC_VERBOSE
// env var both control verbosity — if either is true, verbose logging is on.
func Logger(cfg ...LoggingConfig) func(http.Handler) http.Handler {
	var conf LoggingConfig
	if len(cfg) > 0 {
		conf = cfg[0]
	}

	mode := os.Getenv("GOTHIC_MODE")
	provider := os.Getenv("GOTHIC_PROVIDER")
	envVerbose := os.Getenv("GOTHIC_VERBOSE")

	verbose := conf.Verbose || envVerbose == "true"

	// In dev mode with no verbose flag, skip all logging.
	if mode == "dev" && !verbose {
		return func(next http.Handler) http.Handler {
			return next
		}
	}

	var logger *slog.Logger
	switch provider {
	case "AWS":
		logger = newJSONLogger()
	default:
		logger = newTextLogger()
	}
	logger = wrapLogger(logger)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			rw := rwPool.Get().(*responseWriter)
			rw.ResponseWriter = w
			rw.statusCode = 0

			next.ServeHTTP(rw, r)

			status := rw.statusCode
			if status == 0 {
				status = http.StatusOK
			}
			logRequest(logger, r, status, start)

			rw.ResponseWriter = nil
			rwPool.Put(rw)
		})
	}
}

// logRequest is the hot path — it writes a single structured log line
// using slog.LogAttrs to avoid per-call allocations.
func logRequest(logger *slog.Logger, r *http.Request, status int, start time.Time) {
	logger.LogAttrs(r.Context(), slog.LevelInfo, "request",
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.Int("status", status),
		slog.Duration("duration", time.Since(start)),
		slog.String("remote", r.RemoteAddr),
	)
}

func newJSONLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}

// newTextLogger picks the readable, coloured request line when a developer is
// watching a terminal, and falls back to key=value slog text when the stream is
// redirected, so collected logs stay machine-parseable.
func newTextLogger() *slog.Logger {
	if colorEnabled(os.Stderr) {
		return slog.New(newPrettyHandler(os.Stderr))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}

func wrapLogger(logger *slog.Logger) *slog.Logger {
	return logger.With("component", "gothic")
}
