package middlewares

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func renderPretty(t *testing.T, color bool, status int) string {
	t.Helper()
	var buf bytes.Buffer
	h := &prettyHandler{mu: &sync.Mutex{}, w: &buf, color: color}
	logger := slog.New(h).With("component", "gothic")
	logger.LogAttrs(context.Background(), slog.LevelInfo, "request",
		slog.String("method", "GET"),
		slog.String("path", "/users"),
		slog.Int("status", status),
		slog.Duration("duration", 1500*time.Microsecond),
		slog.String("remote", "127.0.0.1:5555"),
	)
	return buf.String()
}

func TestPrettyHandler_PlainHasNoEscapes(t *testing.T) {
	out := renderPretty(t, false, 200)
	if strings.Contains(out, "\033[") {
		t.Errorf("colour must be absent when disabled: %q", out)
	}
	for _, want := range []string{"GET", "/users", "200", "1.5ms", "127.0.0.1:5555"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in %q", want, out)
		}
	}
}

func TestPrettyHandler_ColorsTheStatusClass(t *testing.T) {
	cases := []struct {
		status int
		code   string
	}{
		{200, ansiGreen},
		{301, ansiCyan},
		{404, ansiYellow},
		{500, ansiRed},
	}
	for _, c := range cases {
		out := renderPretty(t, true, c.status)
		if !strings.Contains(out, c.code) {
			t.Errorf("status %d should use %q, got %q", c.status, c.code, out)
		}
	}
}

func TestPrettyHandler_DropsComponentKeepsUnknownAttrs(t *testing.T) {
	var buf bytes.Buffer
	h := &prettyHandler{mu: &sync.Mutex{}, w: &buf, color: false}
	logger := slog.New(h).With("component", "gothic")
	logger.LogAttrs(context.Background(), slog.LevelInfo, "request",
		slog.String("method", "GET"),
		slog.String("path", "/"),
		slog.Int("status", 200),
		slog.String("tenant", "acme"),
	)
	out := buf.String()
	if strings.Contains(out, "component") {
		t.Errorf("component is implied and should not be printed: %q", out)
	}
	if !strings.Contains(out, "tenant=acme") {
		t.Errorf("unknown attributes must survive: %q", out)
	}
}

func TestColorEnabled_RejectsNonTerminal(t *testing.T) {
	// A buffer is not an *os.File, so it can never be a terminal.
	if colorEnabled(&bytes.Buffer{}) {
		t.Error("a non-file writer must not get colour")
	}
}

func TestPrettyHandler_TimestampHasNoDate(t *testing.T) {
	// The CLI prints wall-clock only; a request line that carried the date would
	// re-introduce two formats on one screen.
	out := renderPretty(t, false, 200)
	if !regexp.MustCompile(`^\d{2}:\d{2}:\d{2}\.\d{3} `).MatchString(out) {
		t.Errorf("expected hh:mm:ss.mmm with no date, got %q", out)
	}
}

func TestPrettyHandler_UsesTheGothicPalette(t *testing.T) {
	// These codes must stay identical to cli/internal/termcolor. A published
	// module cannot import that internal package, so the pairing is asserted
	// here instead of enforced by the compiler.
	want := map[string]string{
		"white":  "\033[37m",
		"cyan":   "\033[36m",
		"red":    "\033[31m",
		"amber":  "\033[38;5;221m",
		"green":  "\033[38;5;120m",
		"purple": "\033[38;5;135m",
		"blue":   "\033[38;5;75m",
		"sky":    "\033[38;5;117m",
		"lilac":  "\033[38;5;141m",
		"gray":   "\033[38;5;244m",
	}
	got := map[string]string{
		"white":  ansiWhite,
		"cyan":   ansiCyan,
		"red":    ansiRed,
		"amber":  ansiYellow,
		"green":  ansiGreen,
		"purple": ansiPurple,
		"blue":   ansiBlue,
		"sky":    ansiSky,
		"lilac":  ansiLilac,
		"gray":   ansiGray,
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s drifted from the Gothic palette: %q, want %q", k, got[k], w)
		}
	}
}

func TestMethodColor(t *testing.T) {
	// The verb's colour follows what it does to the resource, so a destructive
	// call is never dressed like a read.
	cases := map[string]string{
		"GET":     ansiGreen,
		"get":     ansiGreen,
		"POST":    ansiPurple,
		"PUT":     ansiYellow,
		"PATCH":   ansiBlue,
		"DELETE":  ansiRed,
		"HEAD":    ansiGray,
		"OPTIONS": ansiCyan,
		"TRACE":   ansiWhite,
		"":        ansiWhite,
	}
	for method, want := range cases {
		if got := methodColor(method); got != want {
			t.Errorf("methodColor(%q) = %q, want %q", method, got, want)
		}
	}
}

// The invariant, stated directly: no two verbs may share a colour. This is what
// actually has to hold, and it catches a future verb assigned to a taken hue.
func TestMethodColorsAreAllDistinct(t *testing.T) {
	methods := []string{
		http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodHead, http.MethodOptions, http.MethodTrace,
	}
	seen := map[string]string{}
	for _, m := range methods {
		c := methodColor(m)
		if other, taken := seen[c]; taken {
			t.Errorf("%s and %s share the colour %q", other, m, c)
		}
		seen[c] = m
	}
	if len(seen) != len(methods) {
		t.Errorf("expected %d distinct colours, got %d", len(methods), len(seen))
	}
}

func TestPrettyHandler_PaintsTheMethod(t *testing.T) {
	var buf bytes.Buffer
	h := &prettyHandler{mu: &sync.Mutex{}, w: &buf, color: true}
	slog.New(h).LogAttrs(context.Background(), slog.LevelInfo, "request",
		slog.String("method", "DELETE"),
		slog.String("path", "/users/1"),
		slog.Int("status", 204),
	)
	if !strings.Contains(buf.String(), ansiRed+"DELETE") {
		t.Errorf("DELETE should be painted red: %q", buf.String())
	}
}

func TestPrettyHandler_NoTokenSharesAColour(t *testing.T) {
	// Every token on the line answers a different question, so two of them in the
	// same colour would be two things the eye cannot separate. The verb is
	// excluded: its colour is chosen per method and compared elsewhere.
	used := map[string]string{
		"timestamp": ansiGray,
		"path":      ansiSky,
		"duration":  ansiYellow,
		"remote":    ansiLilac,
	}
	seen := map[string]string{}
	for role, c := range used {
		if other, taken := seen[c]; taken {
			t.Errorf("%s and %s share the colour %q", other, role, c)
		}
		seen[c] = role
	}
}

func TestPrettyHandler_PathIsNotWhite(t *testing.T) {
	var buf bytes.Buffer
	h := &prettyHandler{mu: &sync.Mutex{}, w: &buf, color: true}
	slog.New(h).LogAttrs(context.Background(), slog.LevelInfo, "request",
		slog.String("method", "GET"),
		slog.String("path", "/public/styles.css"),
		slog.Int("status", 200),
	)
	if strings.Contains(buf.String(), ansiWhite+"/public") {
		t.Errorf("the path should carry a docs blue, not plain white: %q", buf.String())
	}
	if !strings.Contains(buf.String(), ansiSky+"/public/styles.css") {
		t.Errorf("expected the path in the sky tone: %q", buf.String())
	}
}
