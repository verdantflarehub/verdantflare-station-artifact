package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestArtifactListenerIsolation(t *testing.T) {
	service := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(202) })
	health := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	mcp := artifactListenerHandler(service, health, true, false)
	business := artifactListenerHandler(service, health, false, true)
	legacy := artifactListenerHandler(service, health, false, false)
	for _, tc := range []struct {
		handler      http.Handler
		method, path string
		status       int
	}{
		{mcp, "POST", "/mcp", 202}, {mcp, "GET", "/health", 204}, {mcp, "POST", "/health", 405},
		{mcp, "GET", "/v2/artifacts/item/content", 404}, {mcp, "POST", "/v2/artifacts/retentions", 404},
		{mcp, "POST", "/mcp?other=1", 404}, {mcp, "POST", "/mcp?", 404}, {mcp, "POST", "/%6dcp", 404},
		{business, "POST", "/mcp", 404}, {business, "POST", "/mcp/tools", 404}, {business, "POST", "/%6dcp", 404},
		{business, "POST", "/v2/artifacts/uploads", 202}, {business, "GET", "/health", 204},
		{legacy, "POST", "/mcp", 202}, {legacy, "GET", "/v2/artifacts/item/content", 202},
	} {
		out := httptest.NewRecorder()
		tc.handler.ServeHTTP(out, httptest.NewRequest(tc.method, tc.path, nil))
		if out.Code != tc.status {
			t.Fatalf("%s %s: %d, want %d", tc.method, tc.path, out.Code, tc.status)
		}
	}
}

func TestArtifactListenerConfiguration(t *testing.T) {
	for _, tc := range []struct {
		business, mcp, advertise, endpoints string
		tls, valid                          bool
	}{
		{"0.0.0.0:8094", "", "https://artifact:8094/mcp", "http://etcd:2379", true, true},
		{"0.0.0.0:8094", "", "", "", true, true},
		{"0.0.0.0:8094", "0.0.0.0:8194", "https://artifact:8194/mcp", "http://etcd:2379", true, true},
		{"0.0.0.0:8094", "0.0.0.0:8094", "https://artifact:8094/mcp", "http://etcd:2379", true, false},
		{"0.0.0.0:8094", "0.0.0.0:8194", "http://artifact:8194/mcp", "http://etcd:2379", false, false},
		{"0.0.0.0:8094", "0.0.0.0:8194", "http://artifact:8194/mcp", "http://etcd:2379", true, false},
		{"0.0.0.0:8094", "0.0.0.0:8194", "https://artifact:8194/mcp?", "http://etcd:2379", true, false},
		{"0.0.0.0:8094", "0.0.0.0:8194", "https://artifact:8194/mcp", "", true, false},
	} {
		err := artifactListenerConfig(tc.business, tc.mcp, tc.advertise, tc.endpoints, tc.tls)
		if (err == nil) != tc.valid {
			t.Fatalf("listener validity %v, want %v", err == nil, tc.valid)
		}
	}
}
