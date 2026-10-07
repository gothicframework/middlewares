package middlewares

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// Logger's argument is variadic so the documented call is Logger() with no
// config at all. Every existing test passes a LoggingConfig, leaving the
// zero-argument form — the one a generated main.go actually writes — unexercised.

func TestLoggerNoArgsPassesRequestsThrough(t *testing.T) {
	t.Setenv("GOTHIC_MODE", "dev")
	t.Setenv("GOTHIC_VERBOSE", "")
	t.Setenv("GOTHIC_PROVIDER", "")

	mw := Logger()
	if mw == nil {
		t.Fatal("Logger() returned nil middleware")
	}

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		w.Write([]byte("ok"))
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusTeapot)
	}
	if rec.Body.String() != "ok" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "ok")
	}
}

// The zero-argument form must also work on the logging paths, not just the dev
// no-op shortcut: with GOTHIC_MODE unset the middleware builds a real handler.
func TestLoggerNoArgsOnLoggingPaths(t *testing.T) {
	for _, provider := range []string{"", "AWS"} {
		name := provider
		if name == "" {
			name = "text"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("GOTHIC_MODE", "")
			t.Setenv("GOTHIC_VERBOSE", "")
			t.Setenv("GOTHIC_PROVIDER", provider)

			out := captureStderr(t, func() {
				handler := Logger()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
				if rec.Code != http.StatusOK {
					t.Errorf("status = %d, want 200", rec.Code)
				}
			})

			if out == "" {
				t.Error("expected a log line on stderr, got nothing")
			}
		})
	}
}

// colorEnabled has to survive a writer whose Stat fails. A closed *os.File is
// the reachable case: the type assertion succeeds, then Stat reports the file
// is already closed and there is no FileInfo to inspect.
func TestColorEnabledOnFileWithFailingStat(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("GOTHIC_NO_COLOR", "")

	f, err := os.CreateTemp(t.TempDir(), "stat-*")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close temp file: %v", err)
	}
	if _, err := f.Stat(); err == nil {
		t.Skip("Stat on a closed file succeeded on this platform; nothing to assert")
	}

	if colorEnabled(f) {
		t.Error("colorEnabled = true for a file whose Stat fails, want false")
	}
}

// A regular file on disk is not a character device, so colour stays off — this
// is the branch immediately after the Stat error check.
func TestColorEnabledOnRegularFile(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("GOTHIC_NO_COLOR", "")

	f, err := os.CreateTemp(t.TempDir(), "regular-*")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer f.Close()

	if colorEnabled(f) {
		t.Error("colorEnabled = true for a regular file, want false")
	}
}
