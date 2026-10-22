package webconsole

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConsoleCanonicalRoutesAndFallback(t *testing.T) {
	fallback := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusTeapot)
		_, _ = writer.Write([]byte("fallback:" + request.URL.Path))
	})
	handler := New(fallback)

	for _, target := range []string{"/", "/console", "/console/index.html"} {
		response := performRequest(handler, http.MethodGet, target)
		if response.Code != http.StatusPermanentRedirect {
			t.Fatalf("GET %s status=%d, want 308", target, response.Code)
		}
		if got := response.Header().Get("Location"); got != "/console/" {
			t.Fatalf("GET %s Location=%q", target, got)
		}
	}

	response := performRequest(handler, http.MethodGet, "/v1/runtime")
	if response.Code != http.StatusTeapot || response.Body.String() != "fallback:/v1/runtime" {
		t.Fatalf("fallback status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestConsoleServesExactEmbeddedAssets(t *testing.T) {
	handler := New(http.NotFoundHandler())
	tests := []struct {
		path        string
		contentType string
		contains    string
	}{
		{path: "/console/", contentType: "text/html; charset=utf-8", contains: "AegisRT控制台"},
		{path: "/console/styles.css", contentType: "text/css; charset=utf-8", contains: "[hidden]"},
		{path: "/console/api.js", contentType: "text/javascript; charset=utf-8", contains: "export"},
		{path: "/console/model.js", contentType: "text/javascript; charset=utf-8", contains: "export"},
		{path: "/console/render.js", contentType: "text/javascript; charset=utf-8", contains: "export"},
		{path: "/console/app.js", contentType: "text/javascript; charset=utf-8", contains: "import"},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			response := performRequest(handler, http.MethodGet, test.path)
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
			if got := response.Header().Get("Content-Type"); got != test.contentType {
				t.Fatalf("Content-Type=%q, want %q", got, test.contentType)
			}
			if !strings.Contains(response.Body.String(), test.contains) {
				t.Fatalf("body does not contain %q", test.contains)
			}
			assertConsoleSecurityHeaders(t, response)
		})
	}

	head := performRequest(handler, http.MethodHead, "/console/app.js")
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD status=%d body-bytes=%d", head.Code, head.Body.Len())
	}
}

func TestConsoleRejectsUnknownAssetsMethodsAndTraversal(t *testing.T) {
	handler := New(http.NotFoundHandler())
	for _, target := range []string{
		"/console/missing.js",
		"/console/assets/",
		"/console/%2e%2e/index.html",
		`/console\app.js`,
	} {
		response := performRequest(handler, http.MethodGet, target)
		if response.Code != http.StatusNotFound {
			t.Fatalf("GET %s status=%d, want 404", target, response.Code)
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		response := performRequest(handler, method, "/console/app.js")
		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s status=%d, want 405", method, response.Code)
		}
		if got := response.Header().Get("Allow"); got != "GET, HEAD" {
			t.Fatalf("Allow=%q", got)
		}
	}
}

func TestConsoleAssetsContainNoCredentialOrUnsafeDOMMaterial(t *testing.T) {
	handler := New(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for _, target := range []string{
		"/console/", "/console/styles.css", "/console/api.js",
		"/console/model.js", "/console/render.js", "/console/app.js",
	} {
		response := performRequest(handler, http.MethodGet, target)
		body := response.Body.String()
		for _, forbidden := range []string{
			"credential-sentinel", "localStorage", "sessionStorage", "document.cookie",
			"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write",
			"eval(", "new Function", "http://", "https://",
		} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("%s contains forbidden %q", target, forbidden)
			}
		}
	}

	html := performRequest(handler, http.MethodGet, "/console/").Body.String()
	if strings.Contains(html, "<style") || strings.Contains(html, "<script>") {
		t.Fatal("console HTML contains inline style or script")
	}
}

func performRequest(handler http.Handler, method, target string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertConsoleSecurityHeaders(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	want := map[string]string{
		"Cache-Control":                "no-store",
		"X-Content-Type-Options":       "nosniff",
		"Referrer-Policy":              "no-referrer",
		"X-Frame-Options":              "DENY",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Permissions-Policy":           "camera=(), microphone=(), geolocation=(), usb=()",
	}
	for name, expected := range want {
		if got := response.Header().Get(name); got != expected {
			t.Errorf("%s=%q, want %q", name, got, expected)
		}
	}
	csp := response.Header().Get("Content-Security-Policy")
	for _, directive := range []string{
		"default-src 'none'", "script-src 'self'", "style-src 'self'",
		"connect-src 'self'", "object-src 'none'", "base-uri 'none'",
		"form-action 'none'", "frame-ancestors 'none'",
	} {
		if !strings.Contains(csp, directive) {
			t.Errorf("CSP missing %q: %q", directive, csp)
		}
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("unsafe CSP: %q", csp)
	}
}

func TestConsoleResponseBodiesAreReadable(t *testing.T) {
	response := performRequest(New(http.NotFoundHandler()), http.MethodGet, "/console/")
	if _, err := io.ReadAll(response.Result().Body); err != nil {
		t.Fatal(err)
	}
}
