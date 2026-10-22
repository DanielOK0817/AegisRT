// Package webconsole serves the embedded, same-origin AegisRT operator UI.
package webconsole

import (
	"embed"
	"net/http"
	"strconv"
	"strings"
)

//go:embed assets/*
var assetFS embed.FS

type asset struct {
	contentType string
	content     []byte
}

type handler struct {
	next   http.Handler
	assets map[string]asset
}

// New wraps the control API with exact, embedded Console routes. Static files
// are anonymous, while protected data remains behind the API's Bearer checks.
func New(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	return &handler{next: next, assets: map[string]asset{
		"/console/":           loadAsset("assets/index.html", "text/html; charset=utf-8"),
		"/console/styles.css": loadAsset("assets/styles.css", "text/css; charset=utf-8"),
		"/console/api.js":     loadAsset("assets/api.js", "text/javascript; charset=utf-8"),
		"/console/model.js":   loadAsset("assets/model.js", "text/javascript; charset=utf-8"),
		"/console/render.js":  loadAsset("assets/render.js", "text/javascript; charset=utf-8"),
		"/console/app.js":     loadAsset("assets/app.js", "text/javascript; charset=utf-8"),
	}}
}

func (h *handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	path := request.URL.Path
	if path == "/" || path == "/console" || path == "/console/index.html" {
		setConsoleSecurityHeaders(writer)
		if !allowReadMethod(writer, request) {
			return
		}
		http.Redirect(writer, request, "/console/", http.StatusPermanentRedirect)
		return
	}
	if item, exists := h.assets[path]; exists {
		setConsoleSecurityHeaders(writer)
		if !allowReadMethod(writer, request) {
			return
		}
		writer.Header().Set("Content-Type", item.contentType)
		writer.Header().Set("Content-Length", strconv.Itoa(len(item.content)))
		writer.WriteHeader(http.StatusOK)
		if request.Method == http.MethodGet {
			_, _ = writer.Write(item.content)
		}
		return
	}
	if strings.HasPrefix(path, "/console/") {
		setConsoleSecurityHeaders(writer)
		http.NotFound(writer, request)
		return
	}
	h.next.ServeHTTP(writer, request)
}

func loadAsset(path, contentType string) asset {
	content, err := assetFS.ReadFile(path)
	if err != nil {
		panic("webconsole: embedded asset missing: " + path)
	}
	return asset{contentType: contentType, content: content}
}

func allowReadMethod(writer http.ResponseWriter, request *http.Request) bool {
	if request.Method == http.MethodGet || request.Method == http.MethodHead {
		return true
	}
	writer.Header().Set("Allow", "GET, HEAD")
	http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

func setConsoleSecurityHeaders(writer http.ResponseWriter) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Frame-Options", "DENY")
	writer.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	writer.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	writer.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), usb=()")
	writer.Header().Set("Content-Security-Policy", strings.Join([]string{
		"default-src 'none'", "script-src 'self'", "style-src 'self'",
		"img-src 'self' data:", "connect-src 'self'", "object-src 'none'",
		"base-uri 'none'", "form-action 'none'", "frame-ancestors 'none'",
	}, "; "))
}
