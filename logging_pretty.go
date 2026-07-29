package middlewares

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The canonical Gothic palette, mirrored from the CLI so the request log and
// the build output read as one surface. The codes are duplicated rather than
// imported because the CLI's copy lives in an internal package that a published
// library module cannot reach; they must be kept identical.
const (
	ansiReset  = "\033[0m"
	ansiWhite  = "\033[37m"       // message text / neutral
	ansiCyan   = "\033[36m"       // the OPTIONS verb
	ansiRed    = "\033[31m"       // errors
	ansiYellow = "\033[38;5;221m" // warnings
	ansiGreen  = "\033[38;5;120m" // success
	ansiBlue   = "\033[38;5;75m"  // locations (docs #60a5fa)
	ansiSky    = "\033[38;5;117m" // request path (docs #93c5fd)
	ansiLilac  = "\033[38;5;141m" // caller address (docs #c084fc)
	ansiPurple = "\033[38;5;135m" // Gothic accent
	ansiGray   = "\033[38;5;244m" // dim / secondary text
)

// timestampLayout matches the CLI's own prefix (wall-clock, no date) with
// milliseconds added, so build lines and request lines line up on screen.
const timestampLayout = "15:04:05.000"

// colorEnabled reports whether w is an interactive terminal that has not opted
// out of colour. Anything redirected to a file, a pipe or a log collector gets
// plain text, so no escape sequence ever reaches a stored log.
func colorEnabled(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("GOTHIC_NO_COLOR") != "" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// prettyHandler renders the request log as one readable line for a developer
// watching a terminal. It is scoped to this middleware's own records rather
// than being a general-purpose slog handler.
type prettyHandler struct {
	mu    *sync.Mutex
	w     io.Writer
	attrs []slog.Attr
	color bool
}

func newPrettyHandler(w io.Writer) *prettyHandler {
	return &prettyHandler{mu: &sync.Mutex{}, w: w, color: colorEnabled(w)}
}

func (h *prettyHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelInfo
}

func (h *prettyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := *h
	next.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &next
}

// WithGroup is a no-op: the request logger emits a flat set of attributes.
func (h *prettyHandler) WithGroup(string) slog.Handler { return h }

func (h *prettyHandler) paint(code, s string) string {
	if !h.color {
		return s
	}
	return code + s + ansiReset
}

// methodColor gives every verb its own colour: two verbs that share one are two
// verbs you have to read to tell apart, which defeats the point of colouring
// them. Where the palette already carries a meaning it is honoured — yellow is
// update/replace, red is destroy — and the rest are assigned so that the verbs
// seen most often in a dev log land on the most distinguishable hues.
func methodColor(method string) string {
	switch strings.ToUpper(method) {
	case http.MethodGet:
		return ansiGreen
	case http.MethodPost:
		return ansiPurple
	case http.MethodPut:
		return ansiYellow
	case http.MethodPatch:
		return ansiBlue
	case http.MethodDelete:
		return ansiRed
	case http.MethodHead:
		return ansiGray
	case http.MethodOptions:
		return ansiCyan
	default:
		return ansiWhite
	}
}

// statusColor follows the usual convention: 2xx green, 3xx cyan, 4xx yellow,
// 5xx red, so a failing request is visible without reading the number.
func statusColor(status int) string {
	switch {
	case status >= 500:
		return ansiRed
	case status >= 400:
		return ansiYellow
	case status >= 300:
		return ansiCyan
	default:
		return ansiGreen
	}
}

func (h *prettyHandler) Handle(_ context.Context, r slog.Record) error {
	var method, path, remote string
	var status int
	var dur time.Duration
	var extra []string

	collect := func(a slog.Attr) {
		switch a.Key {
		case "method":
			method = a.Value.String()
		case "path":
			path = a.Value.String()
		case "remote":
			remote = a.Value.String()
		case "status":
			status = int(a.Value.Int64())
		case "duration":
			dur = a.Value.Duration()
		case "component":
			// Implied by the surface printing the line.
		default:
			extra = append(extra, a.Key+"="+a.Value.String())
		}
	}
	for _, a := range h.attrs {
		collect(a)
	}
	r.Attrs(func(a slog.Attr) bool {
		collect(a)
		return true
	})

	var b strings.Builder
	// Same layout as the CLI's build lines, plus milliseconds: a page load fires
	// a dozen requests inside one second, so the fraction is what orders them.
	b.WriteString(h.paint(ansiGray, r.Time.Format(timestampLayout)))
	b.WriteString(" ")
	b.WriteString(h.paint(methodColor(method), method))
	b.WriteString(" ")
	b.WriteString(h.paint(ansiSky, path))
	b.WriteString(" ")
	b.WriteString(h.paint(statusColor(status), strconv.Itoa(status)))
	b.WriteString(" ")
	b.WriteString(h.paint(ansiYellow, dur.String()))
	if remote != "" {
		b.WriteString(" ")
		b.WriteString(h.paint(ansiLilac, remote))
	}
	for _, e := range extra {
		b.WriteString(" ")
		b.WriteString(h.paint(ansiGray, e))
	}
	b.WriteString("\n")

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	if err != nil {
		return fmt.Errorf("pretty log write: %w", err)
	}
	return nil
}
