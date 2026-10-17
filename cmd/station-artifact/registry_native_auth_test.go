package main

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type authenticatedRegistryFixture struct {
	pb.UnimplementedAuthServer
	pb.UnimplementedKVServer
	denied         bool
	authCalls      atomic.Int32
	anonymousCalls atomic.Int32
}

func (f *authenticatedRegistryFixture) Authenticate(_ context.Context, in *pb.AuthenticateRequest) (*pb.AuthenticateResponse, error) {
	f.authCalls.Add(1)
	if in.Name != "reader-test" || in.Password != "synthetic-password" {
		return nil, status.Error(codes.Unauthenticated, "rejected")
	}
	return &pb.AuthenticateResponse{Token: "fixture-token"}, nil
}
func (f *authenticatedRegistryFixture) Range(ctx context.Context, _ *pb.RangeRequest) (*pb.RangeResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if len(md.Get("token")) != 1 || md.Get("token")[0] != "fixture-token" {
		f.anonymousCalls.Add(1)
		return nil, status.Error(codes.Unauthenticated, "missing identity")
	}
	if f.denied {
		return nil, status.Error(codes.PermissionDenied, "denied")
	}
	return &pb.RangeResponse{Header: &pb.ResponseHeader{Revision: 1}}, nil
}
func TestRegistryNativeAuthenticatedReadAndDenial(t *testing.T) {
	for _, denied := range []bool{false, true} {
		t.Run(map[bool]string{false: "read", true: "denial"}[denied], func(t *testing.T) {
			listener := bufconn.Listen(1024 * 1024)
			server := grpc.NewServer()
			fixture := &authenticatedRegistryFixture{denied: denied}
			pb.RegisterAuthServer(server, fixture)
			pb.RegisterKVServer(server, fixture)
			go server.Serve(listener)
			t.Cleanup(server.Stop)
			t.Cleanup(func() { _ = listener.Close() })
			config, err := registryClientConfig([]string{"http://registry.test:2379"}, func(key string) string {
				if key == "ETCD_USERNAME" {
					return "reader-test"
				}
				return "synthetic-password"
			})
			if err != nil {
				t.Fatal(err)
			}
			config.DialOptions = append(config.DialOptions, grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
			client, err := clientv3.New(config)
			if err != nil {
				t.Fatal("authenticated client could not connect")
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err = client.Get(ctx, "/verdantflare/mcp/services/image")
			if denied && status.Code(err) != codes.PermissionDenied {
				t.Fatalf("expected permission denial, got %v", err)
			}
			if !denied && err != nil {
				t.Fatal(err)
			}
			if fixture.authCalls.Load() != 1 || fixture.anonymousCalls.Load() != 0 {
				t.Fatal("native authentication missing or anonymous fallback attempted")
			}
		})
	}
}
