package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoopbackHostGuardAllowsOnlyLoopbackAuthorities(t *testing.T) {
	tests := []struct {
		name string
		host string
		want int
	}{
		{name: "IPv4 with port", host: "127.0.0.1:8080", want: http.StatusNoContent},
		{name: "IPv4 without port", host: "127.0.0.2", want: http.StatusNoContent},
		{name: "IPv6 with port", host: "[::1]:8080", want: http.StatusNoContent},
		{name: "IPv6 without port", host: "[::1]", want: http.StatusNoContent},
		{name: "localhost", host: "localhost:8080", want: http.StatusNoContent},
		{name: "localhost case", host: "LOCALHOST", want: http.StatusNoContent},
		{name: "external", host: "example.test:8080", want: http.StatusMisdirectedRequest},
		{name: "suffix attack", host: "localhost.example.test", want: http.StatusMisdirectedRequest},
		{name: "IP suffix attack", host: "127.0.0.1.example.test", want: http.StatusMisdirectedRequest},
		{name: "invalid port", host: "localhost:not-a-port", want: http.StatusMisdirectedRequest},
		{name: "empty", host: "", want: http.StatusMisdirectedRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			handler := loopbackHostGuard(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				called = true
				writer.WriteHeader(http.StatusNoContent)
			}))
			request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/healthz", nil)
			request.Host = test.host
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status=%d, want %d", response.Code, test.want)
			}
			if called != (test.want == http.StatusNoContent) {
				t.Fatalf("downstream called=%v", called)
			}
			if test.want != http.StatusNoContent && response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("rejection is missing no-store")
			}
		})
	}
}
