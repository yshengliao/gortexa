package httpcompat

import (
	"net/http"
	"os"

	"github.com/yshengliao/gortexa/internal/config"
)

// CORS wraps next with permissive CORS handling when enabled in config. It
// short-circuits preflight OPTIONS requests.
func CORS(next http.Handler, cfg config.ServerConfig) http.Handler {
	if !cfg.EnableCORS {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-Id")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// OpenAPIHandler serves a swagger/OpenAPI JSON document from disk if present.
// The path is typically gen/openapiv2/gortexa.swagger.json (produced by make gen).
func OpenAPIHandler(path string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		spec, err := os.ReadFile(path)
		if err != nil {
			http.Error(w, `{"code":"not_found","message":"openapi spec unavailable"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(spec)
	})
}
