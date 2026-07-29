package middlewares

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// captureStderr redirects os.Stderr for the duration of fn and returns what was
// written. Used to capture slog output from the Logger middleware.
// IMPORTANT: Call Logger() INSIDE fn so the slog handler is created with the
// redirected stderr — slog captures the os.Stderr *os.File value at construction.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	fn()

	w.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("copy: %v", err)
	}
	return buf.String()
}

// TestLogger_DevModeSilent asserts that in dev mode with no verbose flag the
// Logger middleware is a no-op passthrough — the handler response is preserved.
func TestLogger_DevModeSilent(t *testing.T) {
	t.Setenv("GOTHIC_MODE", "dev")

	handler := Logger(LoggingConfig{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Errorf("expected body 'ok', got %q", rec.Body.String())
	}
}

// TestLogger_AWSJSONFormat asserts that when GOTHIC_PROVIDER=AWS the Logger
// produces JSON-formatted log output (the JSON handler is used). Logger()
// is called inside captureStderr so the slog handler is created with the
// redirected stderr pipe.
func TestLogger_AWSJSONFormat(t *testing.T) {
	t.Setenv("GOTHIC_MODE", "")
	t.Setenv("GOTHIC_PROVIDER", "AWS")

	out := captureStderr(t, func() {
		handler := Logger(LoggingConfig{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		req := httptest.NewRequest("GET", "/test", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	})

	// JSON output starts with {; text output starts with time=
	if !strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("expected JSON log output starting with '{', got: %s", out)
	}
}

// TestLogger_ResponseWriterStatus verifies that responseWriter correctly captures
// the HTTP status code from both WriteHeader and Write (which defaults to 200).
func TestLogger_ResponseWriterStatus(t *testing.T) {
	t.Run("WriteHeader(404) captures status", func(t *testing.T) {
		rec := httptest.NewRecorder()
		rw := &responseWriter{ResponseWriter: rec}
		rw.WriteHeader(http.StatusNotFound)
		if rw.statusCode != http.StatusNotFound {
			t.Errorf("status = %d, want %d", rw.statusCode, http.StatusNotFound)
		}
	})

	t.Run("Write defaults to 200", func(t *testing.T) {
		rec := httptest.NewRecorder()
		rw := &responseWriter{ResponseWriter: rec}
		n, err := rw.Write([]byte("hello"))
		if err != nil {
			t.Fatalf("Write error: %v", err)
		}
		if n != 5 {
			t.Errorf("Write returned %d, want 5", n)
		}
		if rw.statusCode != http.StatusOK {
			t.Errorf("status after Write = %d, want %d", rw.statusCode, http.StatusOK)
		}
	})
}

// TestLogger_DevModeVerbose asserts that in dev mode with GOTHIC_VERBOSE=true
// the Logger produces log output. Logger() is called inside captureStderr so
// the slog handler captures the redirected stderr pipe.
func TestLogger_DevModeVerbose(t *testing.T) {
	t.Setenv("GOTHIC_MODE", "dev")
	t.Setenv("GOTHIC_VERBOSE", "true")

	out := captureStderr(t, func() {
		handler := Logger(LoggingConfig{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ok"))
		}))
		req := httptest.NewRequest("GET", "/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	})

	if !strings.Contains(out, "request") {
		t.Errorf("expected log output in verbose dev mode, got: %s", out)
	}
}

// TestLogger_NonDevAlwaysLogs asserts that when GOTHIC_MODE is not set (or set
// to any value other than "dev") the Logger always produces log output, even
// without any verbose flag.
func TestLogger_NonDevAlwaysLogs(t *testing.T) {
	t.Setenv("GOTHIC_MODE", "")

	out := captureStderr(t, func() {
		handler := Logger(LoggingConfig{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		req := httptest.NewRequest("GET", "/test", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	})

	if !strings.Contains(out, "request") {
		t.Errorf("expected log output in production mode, got: %s", out)
	}
}

// TestLogger_DevModeVerboseFlag asserts that in dev mode with the
// LoggingConfig.Verbose flag set to true (but no GOTHIC_VERBOSE env var) the
// Logger produces log output.
func TestLogger_DevModeVerboseFlag(t *testing.T) {
	t.Setenv("GOTHIC_MODE", "dev")

	out := captureStderr(t, func() {
		handler := Logger(LoggingConfig{Verbose: true})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ok"))
		}))
		req := httptest.NewRequest("GET", "/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	})

	if !strings.Contains(out, "request") {
		t.Errorf("expected log output when verbose flag set in dev mode, got: %s", out)
	}
}

// flushRecorder wraps httptest.ResponseRecorder and implements http.Flusher.
type flushRecorder struct {
	*httptest.ResponseRecorder
	flushed bool
}

func (f *flushRecorder) Flush() {
	f.flushed = true
}

// TestLogger_FlusherPassthrough asserts that the responseWriter passes Flush
// through to the underlying writer when it supports http.Flusher, and that
// calling Flush on a non-flushing writer does not panic.
func TestLogger_FlusherPassthrough(t *testing.T) {
	t.Run("flush forwarded to flushing writer", func(t *testing.T) {
		t.Setenv("GOTHIC_MODE", "dev")
		t.Setenv("GOTHIC_VERBOSE", "true")

		fr := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
		handler := Logger(LoggingConfig{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ok"))
		}))

		req := httptest.NewRequest("GET", "/", nil)
		handler.ServeHTTP(fr, req)

		if !fr.flushed {
			t.Error("expected Flush to be forwarded through middleware")
		}
	})

	t.Run("flush on non-flushing writer does not panic", func(t *testing.T) {
		t.Setenv("GOTHIC_MODE", "dev")
		t.Setenv("GOTHIC_VERBOSE", "true")

		// httptest.NewRecorder does NOT implement http.Flusher.
		handler := Logger(LoggingConfig{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Calling Flush on a non-Flusher must not panic
			// (responseWriter.Flush checks the type assertion).
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			w.WriteHeader(http.StatusOK)
		}))

		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/", nil)
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rec.Code)
		}
	})
}

// TestLogger_NoBodyStatus asserts that a handler which calls WriteHeader
// without writing a body is logged with the correct status code (not 0).
func TestLogger_NoBodyStatus(t *testing.T) {
	t.Setenv("GOTHIC_MODE", "")

	out := captureStderr(t, func() {
		handler := Logger(LoggingConfig{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			// Deliberately no body write — status must still be 404.
		}))
		req := httptest.NewRequest("GET", "/missing", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	})

	// Text format:  status=404
	// JSON format:  "status":404
	if !strings.Contains(out, "status=404") && !strings.Contains(out, `"status":404`) {
		t.Errorf("expected log to contain status=404, got: %s", out)
	}
}
