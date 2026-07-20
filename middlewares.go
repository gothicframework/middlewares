// Package middlewares exposes the Gothic runtime as a single chi middleware, driven by
// the Runtime block declared in gothic.config.go. Applied like any chi middleware
// (router.Use), it keeps main.go tiny while new built-in route features light up
// automatically on a framework upgrade — the user never edits main.go.
package middlewares

import (
	"io/fs"
	"net/http"
	"strings"

	gothicComponents "github.com/gothicframework/components"
	"github.com/gothicframework/core/config"
	gothicRoutes "github.com/gothicframework/core/router"
	"github.com/gothicframework/core/runtimeassets"

	"github.com/go-chi/chi/v5"
)

// SetEmbeddedPublicFS registers the embed.FS backing /public/* when the app's
// RuntimeConfig uses ServeStaticFiles == EMBEDDED. It forwards into the core
// router. The generated root embed file (which owns the //go:embed public
// directive) calls this from init(), before main(); user main.go imports this
// package, not core/router directly. fsys MUST already be rooted at the public
// dir (fs.Sub'd).
func SetEmbeddedPublicFS(fsys fs.FS) { gothicRoutes.SetEmbeddedPublicFS(fsys) }

// Middleware returns a chi middleware — applied like router.Use(middleware.Logger)
// — that wires the whole Gothic runtime from a single RuntimeConfig: it initializes
// the cache backend once, then serves the framework's built-in paths (/public/*
// static assets and the /optimizedImage/* endpoint), letting every other request
// fall through to the routes you register on the router. New built-in route
// features are added here and appear automatically.
func Middleware(cfg config.RuntimeConfig) func(http.Handler) http.Handler {
	// Gothic's built-in routes live on their own mux so they keep full chi routing
	// (OptimizedImage relies on chi.URLParam, which a bare Use middleware can't
	// provide). Setup also initializes the process-wide cache backend and mounts
	// /public/* static serving per the config; the empty registrar means only
	// Gothic's own routes are on this internal mux — the user's file-based routes
	// stay on the main router and are reached via next.
	internal := chi.NewMux()
	gothicRoutes.Setup(internal, cfg, func(chi.Router) {})
	gothicComponents.OptimizedImageConfig.RegisterRoute(internal, "/optimizedImage/{name}/{extension}", gothicComponents.OptimizedImage)
	// Serve the framework's WASM-runtime assets (gothic-core.js/.wasm, the two
	// exec shims, the boot loader) straight from the framework embed under
	// /_gothic/* instead of copying them into every project's public/ folder.
	internal.Handle(runtimeassets.Prefix+"*", runtimeassets.Handler())

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if p := r.URL.Path; strings.HasPrefix(p, "/public/") || strings.HasPrefix(p, "/optimizedImage/") || strings.HasPrefix(p, runtimeassets.Prefix) {
				internal.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
