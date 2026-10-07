package middlewares

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	gothicRoutes "github.com/gothicframework/core/router"
)

// trace.go is the dev-mode server request tracer. At construction (dev mode
// only, GOTHIC_MODE evaluated once) it installs the router.TraceHook observer
// and wraps the handler chain: every request gets a trace record with its
// ordered stages, kept in a bounded ring buffer served by the
// /_gothicframework/trace endpoint. Outside dev mode nothing is installed and
// the middleware chain is returned unchanged.

const (
	// traceCapacity bounds the ring buffer of recent requests.
	traceRingCapacity = 200
	// tracePayloadLimit bounds the In/Out strings of stored stages.
	tracePayloadCap = 2048
)

// traceStage mirrors one stage of a request within a trace record. Field
// names follow the trace record contract (kind, name, ms, bytes, cache, in, out).
type traceStage struct {
	Kind  string `json:"kind"` // "middleware" | "render" | "injection" | "api"
	Name  string `json:"name"`
	MS    int64  `json:"ms"`
	Bytes int    `json:"bytes,omitempty"`
	Cache string `json:"cache,omitempty"`
	In    string `json:"in,omitempty"`
	Out   string `json:"out,omitempty"`
}

// traceRecord is one traced request as served by the /_gothicframework/trace
// endpoint (JSON array, newest first).
type traceRecord struct {
	ID      string       `json:"id"`
	TS      int64        `json:"ts"` // Unix milliseconds
	Method  string       `json:"method"`
	Path    string       `json:"path"`
	Route   string       `json:"route"`
	Type    string       `json:"type"`
	Status  int          `json:"status"`
	TotalMS int64        `json:"total_ms"`
	Stages  []traceStage `json:"stages"`
}

// traceCollectorKey names the request-context value the tracer wrapper
// installs so hook events can be attributed to their request.
type traceCollectorKey struct{}

// traceCollector accumulates the stages of one in-flight request.
type traceCollector struct {
	stages []traceStage
}

// requestTracer owns the ring buffer and the TraceHook implementation. All
// methods are safe under concurrent requests.
type requestTracer struct {
	seq   sync.Mutex
	next  int
	count int

	nextID atomic.Int64
	ring   []traceRecord
}

// newRequestTracer creates the tracer and installs gothicRoutes.TraceHook as
// its event source. Events are attributed to their request through the
// collector the wrapper stores in the request context.
func newRequestTracer() *requestTracer {
	t := &requestTracer{ring: make([]traceRecord, traceRingCapacity)}
	gothicRoutes.TraceHook = t.onTraceEvent
	return t
}

// onTraceEvent is the router.TraceHook implementation: it converts a stage
// event into a stored traceStage, attributed via the event's request context.
// Events without a traced request context are dropped (nothing to attribute).
func (t *requestTracer) onTraceEvent(ev gothicRoutes.TraceEvent) {
	ctx := ev.Ctx()
	if ctx == nil {
		return
	}
	col, _ := ctx.Value(traceCollectorKey{}).(*traceCollector)
	if col == nil {
		return
	}
	col.stages = append(col.stages, traceStage{
		Kind:  ev.Kind,
		Name:  ev.Name,
		MS:    ev.MS,
		Bytes: ev.Bytes,
		Cache: ev.Cache,
		In:    traceSanitize(ev.In),
		Out:   traceSanitize(ev.Out),
	})
}

// wrap instruments the handler chain: it installs a per-request collector,
// captures status and total time, derives the matched chi route pattern, and
// stores the finished record in the ring.
func (t *requestTracer) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		col := new(traceCollector)
		r = r.WithContext(context.WithValue(r.Context(), traceCollectorKey{}, col))
		rw := &responseWriter{ResponseWriter: w}

		next.ServeHTTP(rw, r)

		status := rw.statusCode
		if status == 0 {
			status = http.StatusOK
		}
		id := "req-" + strconv.FormatInt(t.nextID.Add(1), 10)
		t.store(traceRecord{
			ID:      id,
			TS:      start.UnixMilli(),
			Method:  r.Method,
			Path:    r.URL.Path,
			Route:   chi.RouteContext(r.Context()).RoutePattern(),
			Type:    traceStageType(col.stages),
			Status:  status,
			TotalMS: time.Since(start).Milliseconds(),
			Stages:  col.stages,
		})
	})
}

// traceStageType derives the request-level route type from the observed
// stages: the middleware/api stage name carries DYNAMIC/STATIC/ISR.
func traceStageType(stages []traceStage) string {
	for _, s := range stages {
		if s.Kind == "middleware" || s.Kind == "api" {
			return s.Name
		}
	}
	return ""
}

// store appends a record to the circular buffer, evicting the oldest entry at
// capacity.
func (t *requestTracer) store(rec traceRecord) {
	t.seq.Lock()
	defer t.seq.Unlock()
	t.ring[t.next] = rec
	t.next = (t.next + 1) % traceRingCapacity
	if t.count < traceRingCapacity {
		t.count++
	}
}

// snapshot returns up to n newest records. n <= 0 returns the whole buffer.
// Records are newest first.
func (t *requestTracer) snapshot(n int) []traceRecord {
	t.seq.Lock()
	defer t.seq.Unlock()
	if n <= 0 || n > t.count {
		n = t.count
	}
	out := make([]traceRecord, 0, n)
	for k := 0; k < n; k++ {
		idx := (t.next - 1 - k + 2*traceRingCapacity) % traceRingCapacity
		out = append(out, t.ring[idx])
	}
	return out
}

// serveTrace is the /_gothicframework/trace endpoint: a JSON array of trace
// records, newest first, bounded by the optional ?last=N query parameter.
func (t *requestTracer) serveTrace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	last := traceRingCapacity
	if q := r.URL.Query().Get("last"); q != "" {
		if n, err := strconv.Atoi(q); err != nil || n < 0 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid last parameter"}`))
			return
		} else {
			last = n
		}
	}
	records := t.snapshot(last)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(records)
}

// traceSensitiveRE matches assignment-style occurrences of secret-bearing
// keys; the captured prefix preserves the key, only the value is redacted.
var traceSensitiveRE = regexp.MustCompile(
	`(?i)((?:password|token|secret|authorization|cookie|api_?key)["']?\s*[:=]\s*)("(?:[^"\\]|\\.)*"|'[^'\n]*'|bearer\s+\S+|[^\s,;&)}\]]+)`)

// traceSanitize redacts known sensitive values and truncates the payload,
// appending the dropped byte count. The cap sits one suffix-width above the
// router layer's own truncation limit, so an already-truncated In/Out string
// passes through intact instead of being cut twice.
func traceSanitize(s string) string {
	if s == "" {
		return ""
	}
	s = traceSensitiveRE.ReplaceAllString(s, `${1}[REDACTED]`)
	const cap = tracePayloadCap + 64
	if len(s) <= cap {
		return s
	}
	return fmt.Sprintf("%s…(+%d bytes)", s[:cap], len(s)-cap)
}
