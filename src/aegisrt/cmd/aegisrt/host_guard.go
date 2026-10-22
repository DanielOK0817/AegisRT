package main

import (
	"net"
	"net/http"
	"strconv"
	"strings"
)

// loopbackHostGuard prevents a browser from reaching the local administrator
// surface through a DNS-rebound non-loopback origin. The listener is already
// loopback-only; this validates the HTTP authority at the outermost boundary.
func loopbackHostGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if validLoopbackAuthority(request.Host) {
			next.ServeHTTP(writer, request)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		http.Error(writer, "loopback Host required", http.StatusMisdirectedRequest)
	})
}

func validLoopbackAuthority(authority string) bool {
	if authority == "" || strings.TrimSpace(authority) != authority {
		return false
	}
	host := authority
	if strings.HasPrefix(authority, "[") {
		closing := strings.IndexByte(authority, ']')
		if closing <= 1 {
			return false
		}
		host = authority[1:closing]
		remainder := authority[closing+1:]
		if remainder != "" && (remainder[0] != ':' || !validHostPort(remainder[1:])) {
			return false
		}
	} else {
		switch strings.Count(authority, ":") {
		case 0:
		case 1:
			separator := strings.LastIndexByte(authority, ':')
			host = authority[:separator]
			if !validHostPort(authority[separator+1:]) {
				return false
			}
		default:
			return false
		}
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func validHostPort(value string) bool {
	port, err := strconv.ParseUint(value, 10, 16)
	return err == nil && port > 0
}
