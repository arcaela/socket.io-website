package wire

import (
	"context"
	"net/http"
	"net/http/httptest"
)

func startTestServer(handler http.HandlerFunc) *httptest.Server {
	return httptest.NewServer(handler)
}

func serverCtx() context.Context {
	return context.Background()
}
