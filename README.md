# Gothic Framework — Middlewares

The Gothic runtime as a single chi middleware for [Gothic Framework](https://github.com/gothicframework/core) apps.

```
github.com/gothicframework/middlewares
```

Add it to a Gothic project:

```bash
go get github.com/gothicframework/middlewares
```

This module builds on the core runtime ([`github.com/gothicframework/core`](https://github.com/gothicframework/core)) and the component catalogue ([`github.com/gothicframework/components`](https://github.com/gothicframework/components)). It exposes a single member, `middlewares.Middleware`.

---

## `middlewares.Middleware`

```go
import "github.com/gothicframework/middlewares"
```

### `Middleware(cfg config.RuntimeConfig) func(http.Handler) http.Handler`

Returns a chi middleware — applied like `router.Use(...)` — that wires the **entire** Gothic runtime from the single `Runtime` block in `gothic.config.go`:

- initializes the process-wide cache backend once (per `RuntimeConfig`);
- serves the framework's built-in paths — `/public/*` static assets, the `/optimizedImage/*` endpoint (with full chi routing for its URL params), and the WASM-runtime assets under `/_gothic/*` served from the framework embed; and
- lets every other request fall through to the file-based routes you register on the main router.

New built-in route features are added here and light up automatically on a framework upgrade — you never edit `main.go`.

```go
router := chi.NewMux()
router.Use(middlewares.Middleware(Config.Runtime)) // whole Gothic runtime
routes.RegisterFileBasedRoutes(router)             // your file-based pages
```

The built-in routes run on their own internal chi mux (so `OptimizedImage` keeps its `chi.URLParam` access); only `/public/*`, `/optimizedImage/*`, and `/_gothic/*` are handled there — everything else passes through to `next`.

---

## Requirements

- **Gothic core `github.com/gothicframework/core`** — provides `config.RuntimeConfig`, the `router` setup, and `runtimeassets`.
- **Gothic components `github.com/gothicframework/components`** — provides the `OptimizedImage` route registration.
- **Go 1.25+** and **`go-chi/chi/v5`**.
