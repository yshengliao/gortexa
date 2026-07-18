package httpcompat

import (
	"net/http"
	"os"

	"github.com/yshengliao/gortexa/config"
)

// CORS wraps next with permissive CORS handling when enabled in config. It
// short-circuits preflight OPTIONS requests.
func CORS(next http.Handler, cfg config.ServerConfig) http.Handler {
	if !cfg.EnableCORS {
		return next
	}

	allowed := make(map[string]bool, len(cfg.CORSOrigins))
	for _, o := range cfg.CORSOrigins {
		allowed[o] = true
	}
	allowAll := allowed["*"]

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		origin := r.Header.Get("Origin")

		if allowAll {
			h.Set("Access-Control-Allow-Origin", "*")
		} else if origin != "" && allowed[origin] {
			h.Set("Access-Control-Allow-Origin", origin)
			h.Add("Vary", "Origin")
		}

		if allowAll || (origin != "" && allowed[origin]) {
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-Id")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// OpenAPIRoute serves the OpenAPI spec at /openapi.json in front of next when
// server.openapi is enabled; otherwise it returns next unchanged. This is the
// composition-root seam that makes the config option effective. The spec is a
// static build artifact (produced by make gen), so it is read once here and
// served from memory rather than re-read from disk on every request.
func OpenAPIRoute(next http.Handler, cfg config.ServerConfig, specPath string) http.Handler {
	if !cfg.OpenAPI {
		return next
	}
	spec, err := os.ReadFile(specPath)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/openapi.json" {
			if err != nil {
				http.Error(w, `{"code":"not_found","message":"openapi spec unavailable"}`, http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(spec)
			return
		}
		next.ServeHTTP(w, r)
	})
}
