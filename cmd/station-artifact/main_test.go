package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/verdantflarehub/verdantflare-station-artifact/internal/testdb"
)

func TestMigrateServeAndShutdown(t *testing.T) {
	db := testdb.New(t)
	t.Setenv("ARTIFACT_DATABASE_URL", testdb.ConnString(db))
	store := uuid.Must(uuid.NewV7()).String()
	t.Setenv("ARTIFACT_STORE_ID", store)
	t.Setenv("ARTIFACT_CONTENT_ROOT", t.TempDir())
	t.Setenv("ARTIFACT_STORAGE_BACKEND", "local")
	token := strings.Repeat("s", 32)
	t.Setenv("ARTIFACT_SERVICE_TOKEN", token)
	t.Setenv("ARTIFACT_AUTHORITY_TOKEN", strings.Repeat("a", 32))
	policy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer policy.Close()
	t.Setenv("ARTIFACT_AUTHORITY_URL", policy.URL)
	t.Setenv("ARTIFACT_MAX_CONTENT_BYTES", "1048576")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	t.Setenv("ARTIFACT_LISTEN_ADDR", addr)
	ctx := context.Background()
	if err := run(ctx, []string{"serve"}); err == nil {
		t.Fatal("serve silently migrated database")
	}
	if err := run(ctx, []string{"migrate"}); err != nil {
		t.Fatal(err)
	}
	var actualDatabase string
	if err := db.QueryRow(ctx, "SELECT current_database() FROM station.artifact_stores").Scan(&actualDatabase); err != nil || actualDatabase != db.Config().ConnConfig.Database {
		t.Fatal("CLI did not migrate its isolated test database")
	}
	if err := run(ctx, []string{"migrate"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARTIFACT_STORE_ID", uuid.Must(uuid.NewV7()).String())
	if err := run(ctx, []string{"serve"}); err == nil {
		t.Fatal("serve accepted wrong store identity")
	}
	t.Setenv("ARTIFACT_STORE_ID", store)
	t.Setenv("ARTIFACT_AUTHORITY_TOKEN", "")
	if err := run(ctx, []string{"serve"}); err == nil {
		t.Fatal("serve accepted missing authority credential")
	}
	t.Setenv("ARTIFACT_AUTHORITY_TOKEN", strings.Repeat("a", 32))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, []string{"serve"}) }()
	client := &http.Client{Timeout: 200 * time.Millisecond}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			t.Fatalf("server exited early: %v", err)
		case <-deadline.C:
			t.Fatal("server did not listen")
		case <-tick.C:
			r, _ := http.NewRequest("POST", "http://"+addr+"/v2/artifacts/uploads", strings.NewReader("{}"))
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("Content-Type", "application/json")
			for _, key := range []string{"X-User-Id", "X-Organization-Id", "X-Request-Id"} {
				r.Header.Set(key, uuid.Must(uuid.NewV7()).String())
			}
			response, err := client.Do(r)
			if err != nil {
				continue
			}
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode != 400 {
				t.Fatalf("unexpected status %d", response.StatusCode)
			}
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("server did not stop")
			}
			return
		}
	}
}

func TestNonLoopbackListen(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8094", false},
		{"[::1]:8094", false},
		{"0.0.0.0:8094", true},
		{":8094", true},
	} {
		if got := nonLoopbackListen(tc.addr); got != tc.want {
			t.Errorf("nonLoopbackListen(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}
