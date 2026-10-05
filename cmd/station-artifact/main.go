package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/verdantflarehub/verdantflare-station-artifact/internal/artifact"
	"github.com/verdantflarehub/verdantflare-station-artifact/internal/authority"
	"github.com/verdantflarehub/verdantflare-station-artifact/internal/httpapi"
	"github.com/verdantflarehub/verdantflare-station-artifact/internal/registry"
	"github.com/verdantflarehub/verdantflare-station-artifact/internal/storage"
	"github.com/verdantflarehub/verdantflare-station-artifact/migrations"
	clientv3 "go.etcd.io/etcd/client/v3"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string) error {
	if len(args) != 1 || (args[0] != "migrate" && args[0] != "serve") {
		return errors.New("usage: station-artifact migrate|serve")
	}
	dsn, store := os.Getenv("ARTIFACT_DATABASE_URL"), os.Getenv("ARTIFACT_STORE_ID")
	if dsn == "" || !artifact.ValidID(store) {
		return errors.New("ARTIFACT_DATABASE_URL and UUIDv7 ARTIFACT_STORE_ID are required")
	}
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	db, err := pgxpool.New(startup, dsn)
	if err != nil {
		return errors.New("invalid Artifact database configuration")
	}
	defer db.Close()
	if err = db.Ping(startup); err != nil {
		return errors.New("Artifact database unavailable")
	}
	if args[0] == "migrate" {
		if err = migrations.Apply(startup, db, store); err != nil {
			return errors.New("Artifact migration failed; check database, migration history and store identity")
		}
		return nil
	}
	root, endpoint, token, authToken := os.Getenv("ARTIFACT_CONTENT_ROOT"), os.Getenv("ARTIFACT_AUTHORITY_URL"), os.Getenv("ARTIFACT_SERVICE_TOKEN"), os.Getenv("ARTIFACT_AUTHORITY_TOKEN")
	if root == "" || token == authToken {
		return errors.New("content root and distinct internal service/authority credentials are required")
	}
	maxSize := int64(1 << 30)
	if raw := os.Getenv("ARTIFACT_MAX_CONTENT_BYTES"); raw != "" {
		maxSize, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || maxSize <= 0 || maxSize > 1<<50 {
			return errors.New("invalid ARTIFACT_MAX_CONTENT_BYTES")
		}
	}
	auth, err := authority.New(endpoint, authToken)
	if err != nil {
		return err
	}
	if err = migrations.Check(startup, db, store); err != nil {
		return errors.New("Artifact migration/store check failed; run migrate explicitly with the correct identity")
	}
	blobs, err := storage.NewLocal(root, maxSize)
	if err != nil {
		return errors.New("Artifact content root unavailable")
	}
	defer blobs.Close()
	service, err := artifact.New(startup, db, blobs, auth, store, maxSize)
	if err != nil {
		return errors.New("Artifact service initialization failed")
	}
	handler, err := httpapi.New(service, token)
	if err != nil {
		return err
	}
	addr := os.Getenv("ARTIFACT_LISTEN_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8094"
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return errors.New("Artifact listener unavailable")
	}
	defer listener.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","service":"station-artifact"}`))
	})
	mux.Handle("/", handler)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Minute, WriteTimeout: 30 * time.Minute, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer server.Close()
	if address := os.Getenv("ARTIFACT_MCP_ADVERTISE_URL"); address != "" {
		u, e := url.Parse(address)
		if e != nil || u.Path != "/mcp" {
			return errors.New("invalid Artifact MCP advertised address")
		}
		endpoints := []string{}
		for _, ep := range strings.Split(os.Getenv("ETCD_ENDPOINTS"), ",") {
			if strings.TrimSpace(ep) != "" {
				endpoints = append(endpoints, strings.TrimSpace(ep))
			}
		}
		if len(endpoints) == 0 {
			return errors.New("ETCD_ENDPOINTS required for Artifact registration")
		}
		cli, e := clientv3.New(clientv3.Config{Endpoints: endpoints, DialTimeout: 5 * time.Second})
		if e != nil {
			return errors.New("invalid Artifact registry configuration")
		}
		defer cli.Close()
		u.Path = "/health"
		version := os.Getenv("ARTIFACT_SERVICE_VERSION")
		if version == "" {
			version = "0.1.0"
		}
		reg := registry.ServiceRegistration{Domain: "artifact", Endpoint: address, HealthEndpoint: u.String(), Version: version, Tools: []registry.ToolDefinition{}}
		for _, tool := range httpapi.MCPTools() {
			reg.Tools = append(reg.Tools, registry.ToolDefinition{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema})
		}
		publication, e := registry.Register(ctx, cli, []registry.ServiceRegistration{reg})
		if e != nil {
			return e
		}
		defer publication.Close()
	}
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errors.New("Artifact HTTP server failed")
	case <-ctx.Done():
		shutdown, finish := context.WithTimeout(context.Background(), 15*time.Second)
		defer finish()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return errors.New("Artifact shutdown deadline exceeded")
		}
		return nil
	}
}
