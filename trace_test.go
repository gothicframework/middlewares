package middlewares

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/gothicframework/core/config"
	gothicRoutes "github.com/gothicframework/core/router"
)

// TestMain initializes the global cache store ONCE for the whole package:
// InitCache is first-call-wins and Middleware() calls it at construction, so
// without this the first test to construct the middleware (non-dev) would pin
// CACHE_CONTROL_HEADERS mode for every later test.
func TestMain(m *testing.M) {
	gothicRoutes.InitCache(gothicRoutes.IN_MEMORY, nil)
	os.Exit(m.Run())
}

// pageComponent is a minimal templ.Component stand-in for route tests.
type pageComponent struct{ html string }

func (c pageComponent) Render(_ context.Context, w io.Writer) error {
	_, err := w.Write([]byte(c.html))
	return err
}

// newTestApp builds a chi mux with the Gothic middleware applied and a
// DYNAMIC page route registered. It returns the mux and the middleware's
// construction-time effects.
func newTestApp(t *testing.T, mode string) *chi.Mux {
	t.Helper()
	t.Setenv("GOTHIC_MODE", mode)
	t.Cleanup(func() { gothicRoutes.TraceHook = nil })

	mux := chi.NewMux()
	mux.Use(Middleware(config.RuntimeConfig{}))

	cfg := gothicRoutes.RouteConfig[string]{
		Type:       gothicRoutes.DYNAMIC,
		HttpMethod: gothicRoutes.GET,
		Middleware: func(w http.ResponseWriter, r *http.Request) string {
			return r.URL.Query().Get("q")
		},
	}
	cfg.RegisterRoute(mux, "/page", func(props string) templ.Component {
		return pageComponent{html: "<p>" + props + "</p>"}
	})
	return mux
}

// get fetches a path through the app and returns the recorder.
func get(mux *chi.Mux, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// fetchTrace fetches the trace endpoint and decodes the JSON array.
func fetchTrace(t *testing.T, mux *chi.Mux, last string) []traceRecord {
	t.Helper()
	path := "/_gothicframework/trace"
	if last != "" {
		path += "?last=" + last
	}
	rec := get(mux, path)
	if rec.Code != http.StatusOK {
		t.Fatalf("trace endpoint = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var records []traceRecord
	if err := json.NewDecoder(rec.Body).Decode(&records); err != nil {
		t.Fatalf("decode trace response: %v", err)
	}
	return records
}

// TestTraceDisabledOutsideDev pins decision D2's off-path: non-dev
// construction installs nothing and the endpoint 404s.
func TestTraceDisabledOutsideDev(t *testing.T) {
	mux := newTestApp(t, "production")
	if gothicRoutes.TraceHook != nil {
		t.Fatal("non-dev construction must not install router.TraceHook")
	}
	rec := get(mux, "/_gothicframework/trace")
	if rec.Code != http.StatusNotFound {
		t.Errorf("trace endpoint in non-dev = %d, want 404", rec.Code)
	}
	// The user route still works untouched.
	if rec := get(mux, "/page"); rec.Code != http.StatusOK || rec.Body.String() != "<p></p>" {
		t.Errorf("passthrough broken: %d %q", rec.Code, rec.Body.String())
	}
}

// TestTraceDisabledEmptyMode also covers the zero-value mode (unset env).
func TestTraceDisabledEmptyMode(t *testing.T) {
	mux := newTestApp(t, "")
	if gothicRoutes.TraceHook != nil {
		t.Fatal("unset GOTHIC_MODE must not install router.TraceHook")
	}
	if rec := get(mux, "/_gothicframework/trace"); rec.Code != http.StatusNotFound {
		t.Errorf("trace endpoint with unset mode = %d, want 404", rec.Code)
	}
}

// TestTraceDevInstallsHookAndEndpoint checks the dev construction seams.
func TestTraceDevInstallsHookAndEndpoint(t *testing.T) {
	mux := newTestApp(t, "dev")
	if gothicRoutes.TraceHook == nil {
		t.Fatal("dev construction must install router.TraceHook")
	}
	if rec := get(mux, "/_gothicframework/trace"); rec.Code != http.StatusOK {
		t.Errorf("trace endpoint in dev = %d, want 200", rec.Code)
	}
}

// TestTraceCaptureAndOrdering walks a DYNAMIC page and asserts the record
// shape: id/ts/method/path/route/type/status/total_ms plus ordered stages.
func TestTraceCaptureAndOrdering(t *testing.T) {
	mux := newTestApp(t, "dev")
	get(mux, "/page?q=hello")

	records := fetchTrace(t, mux, "5")
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	rec := records[0]
	if rec.ID == "" || !strings.HasPrefix(rec.ID, "req-") {
		t.Errorf("id = %q, want req-N", rec.ID)
	}
	if rec.Method != "GET" || rec.Path != "/page" {
		t.Errorf("method/path = %s/%s, want GET//page", rec.Method, rec.Path)
	}
	if rec.Route != "/page" {
		t.Errorf("route = %q, want /page", rec.Route)
	}
	if rec.Type != "DYNAMIC" {
		t.Errorf("type = %q, want DYNAMIC", rec.Type)
	}
	if rec.Status != 200 || rec.TotalMS < 0 || rec.TS <= 0 {
		t.Errorf("status/ts/total_ms = %d/%d/%d", rec.Status, rec.TS, rec.TotalMS)
	}
	if len(rec.Stages) != 2 {
		t.Fatalf("stages = %+v, want 2", rec.Stages)
	}
	if rec.Stages[0].Kind != "middleware" || rec.Stages[0].Name != "DYNAMIC" {
		t.Errorf("stage 0 = %+v, want middleware/DYNAMIC", rec.Stages[0])
	}
	if rec.Stages[0].Out != "hello" {
		t.Errorf("middleware Out = %q, want hello (props from ?q=)", rec.Stages[0].Out)
	}
	if rec.Stages[1].Kind != "render" || rec.Stages[1].Name != "DYNAMIC" {
		t.Errorf("stage 1 = %+v, want render/DYNAMIC", rec.Stages[1])
	}
	if rec.Stages[1].Out != "<p>hello</p>" || rec.Stages[1].Bytes != len("<p>hello</p>") {
		t.Errorf("render stage = %+v, want <p>hello</p>", rec.Stages[1])
	}
}

// TestTraceNewestFirst makes two requests and checks the array order.
func TestTraceNewestFirst(t *testing.T) {
	mux := newTestApp(t, "dev")
	get(mux, "/page?q=first")
	get(mux, "/page?q=second")

	records := fetchTrace(t, mux, "")
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2", len(records))
	}
	if !strings.Contains(records[0].Stages[len(records[0].Stages)-1].Out, "second") {
		t.Errorf("records[0] is not the newest request: %+v", records[0])
	}
	if !strings.Contains(records[1].Stages[len(records[1].Stages)-1].Out, "first") {
		t.Errorf("records[1] is not the older request: %+v", records[1])
	}
}

// TestTraceRedaction asserts secret-bearing keys in stage payloads become
// [REDACTED] in the stored trace.
func TestTraceRedaction(t *testing.T) {
	t.Setenv("GOTHIC_MODE", "dev")
	t.Cleanup(func() { gothicRoutes.TraceHook = nil })

	mux := chi.NewMux()
	mux.Use(Middleware(config.RuntimeConfig{}))
	cfg := gothicRoutes.RouteConfig[string]{
		Type:       gothicRoutes.DYNAMIC,
		HttpMethod: gothicRoutes.GET,
		Middleware: func(w http.ResponseWriter, r *http.Request) string {
			return "token=hushhush" // typical header-style secret assignment
		},
	}
	cfg.RegisterRoute(mux, "/secret", func(props string) templ.Component {
		return pageComponent{html: "<p>x</p>"}
	})

	get(mux, "/secret")

	records := fetchTrace(t, mux, "1")
	stage := records[0].Stages[0]
	if strings.Contains(stage.Out, "hushhush") {
		t.Errorf("secret leaked in stage Out: %q", stage.Out)
	}
	if !strings.Contains(stage.Out, "[REDACTED]") {
		t.Errorf("stage Out lacks [REDACTED]: %q", stage.Out)
	}
}

// TestTraceRedactionAuthorization covers Bearer-token style values.
func TestTraceRedactionAuthorization(t *testing.T) {
	s := traceSanitize("Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload")
	if strings.Contains(s, "eyJ") {
		t.Errorf("bearer token leaked: %q", s)
	}
	s = traceSanitize(`{"password":"hunter2","id":7}`)
	if strings.Contains(s, "hunter2") {
		t.Errorf("password leaked: %q", s)
	}
	if !strings.Contains(s, `"id":7`) {
		t.Errorf("non-sensitive fields must survive: %q", s)
	}
	s = traceSanitize("apiKey=abcd1234")
	if strings.Contains(s, "abcd1234") {
		t.Errorf("apiKey leaked: %q", s)
	}
}

// TestTraceTruncation asserts the 2 KB cap with the dropped byte count.
func TestTraceTruncation(t *testing.T) {
	t.Setenv("GOTHIC_MODE", "dev")
	t.Cleanup(func() { gothicRoutes.TraceHook = nil })

	mux := chi.NewMux()
	mux.Use(Middleware(config.RuntimeConfig{}))
	long := strings.Repeat("a", 3000)
	cfg := gothicRoutes.RouteConfig[string]{
		Type:       gothicRoutes.DYNAMIC,
		HttpMethod: gothicRoutes.GET,
		Middleware: func(w http.ResponseWriter, r *http.Request) string { return long },
	}
	cfg.RegisterRoute(mux, "/big", func(props string) templ.Component {
		return pageComponent{html: "<p>x</p>"}
	})

	get(mux, "/big")

	records := fetchTrace(t, mux, "1")
	out := records[0].Stages[0].Out
	if len(out) > 2100 {
		t.Errorf("stage Out not truncated: %d bytes", len(out))
	}
	if !strings.Contains(out, "…(+952 bytes)") {
		t.Errorf("stage Out lacks the dropped-byte suffix: %q", out[len(out)-32:])
	}
}

// TestTraceRingEviction asserts the buffer holds the newest 200 records.
func TestTraceRingEviction(t *testing.T) {
	mux := newTestApp(t, "dev")
	const requests = traceRingCapacity + 5
	for i := 0; i < requests; i++ {
		get(mux, "/page?q=n")
	}
	records := fetchTrace(t, mux, "999")
	if len(records) != traceRingCapacity {
		t.Fatalf("stored %d records, want %d", len(records), traceRingCapacity)
	}
	// Newest first: the first record must be the last request made.
	first := records[0].Stages[len(records[0].Stages)-1]
	if !strings.Contains(first.Out, "</p>") {
		t.Errorf("newest record missing render stage: %+v", records[0])
	}
	// The ids must strictly decrease across the array (newest → oldest).
	ids := make([]string, len(records))
	for i, r := range records {
		ids[i] = r.ID
	}
	lastNum, _ := strconv.Atoi(strings.TrimPrefix(ids[0], "req-"))
	for _, id := range ids[1:] {
		n, _ := strconv.Atoi(strings.TrimPrefix(id, "req-"))
		if n >= lastNum {
			t.Fatalf("ids not strictly decreasing newest-first: %v", ids[:5])
		}
		lastNum = n
	}
}

// TestTraceLastParamValidation covers the ?last=N parsing.
func TestTraceLastParamValidation(t *testing.T) {
	mux := newTestApp(t, "dev")
	get(mux, "/page")
	if rec := get(mux, "/_gothicframework/trace?last=nope"); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid last = %d, want 400", rec.Code)
	}
	if rec := get(mux, "/_gothicframework/trace?last=0"); rec.Code != http.StatusOK {
		t.Errorf("last=0 = %d, want 200", rec.Code)
	}
	records := fetchTrace(t, mux, "1")
	if len(records) != 1 {
		t.Errorf("last=1 returned %d records, want 1", len(records))
	}
}

// TestTraceCacheHitSkipsMiddlewareStage exercises a STATIC route through the
// middleware in dev: first request shows middleware+miss, second only a hit.
func TestTraceCacheHitSkipsMiddlewareStage(t *testing.T) {
	t.Setenv("GOTHIC_MODE", "dev")
	t.Cleanup(func() { gothicRoutes.TraceHook = nil })

	mux := chi.NewMux()
	mux.Use(Middleware(config.RuntimeConfig{}))
	calls := 0
	cfg := gothicRoutes.RouteConfig[string]{
		Type:       gothicRoutes.STATIC,
		HttpMethod: gothicRoutes.GET,
		Middleware: func(w http.ResponseWriter, r *http.Request) string {
			calls++
			return "s"
		},
	}
	cfg.RegisterRoute(mux, "/s", func(props string) templ.Component {
		return pageComponent{html: "<p>" + props + "</p>"}
	})

	get(mux, "/s")
	get(mux, "/s")
	if calls != 1 {
		t.Fatalf("middleware ran %d times, want 1 (hit must skip it)", calls)
	}

	records := fetchTrace(t, mux, "2")
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2", len(records))
	}
	if len(records[1].Stages) != 2 {
		t.Errorf("first request stages = %+v, want middleware+miss", records[1].Stages)
	}
	if len(records[0].Stages) != 1 || records[0].Stages[0].Cache != "hit" {
		t.Errorf("second request stages = %+v, want single hit", records[0].Stages)
	}
}

// TestTraceApiRouteE2E runs an API endpoint through the middleware and
// asserts the api stage carries route and output payload.
func TestTraceApiRouteE2E(t *testing.T) {
	t.Setenv("GOTHIC_MODE", "dev")
	t.Cleanup(func() { gothicRoutes.TraceHook = nil })

	mux := chi.NewMux()
	mux.Use(Middleware(config.RuntimeConfig{}))
	api := gothicRoutes.ApiRouteConfig{Type: gothicRoutes.DYNAMIC, HttpMethod: gothicRoutes.POST}
	api.RegisterRoute(mux, "/api/echo", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Write(b)
	})

	req := httptest.NewRequest("POST", "/api/echo", strings.NewReader(`{"x":1}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("api echo = %d, want 200", rec.Code)
	}

	records := fetchTrace(t, mux, "1")
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	rec0 := records[0]
	if rec0.Route != "/api/echo" || rec0.Type != "DYNAMIC" || rec0.Status != 200 {
		t.Errorf("record = %+v", rec0)
	}
	if len(rec0.Stages) != 1 {
		t.Fatalf("stages = %+v, want 1", rec0.Stages)
	}
	stage := rec0.Stages[0]
	if stage.Kind != "api" || stage.Name != "DYNAMIC" {
		t.Errorf("stage = %s/%s, want api/DYNAMIC", stage.Kind, stage.Name)
	}
	if stage.Out != `{"x":1}` {
		t.Errorf("stage Out = %q, want echoed body", stage.Out)
	}
}
