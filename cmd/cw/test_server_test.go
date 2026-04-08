package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newIPv4TestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()

	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("tcp4 listen unavailable in this test environment: %v", err)
	}

	srv := httptest.NewUnstartedServer(handler)
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}
