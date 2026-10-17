package registry

import (
	"context"
	clientv3 "go.etcd.io/etcd/client/v3"
	"testing"
	"time"
)

func TestRegistrationChangesUsesExactOwnedKeys(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	streams := map[string]chan clientv3.WatchResponse{}
	watch := func(ctx context.Context, key string, opts ...clientv3.OpOption) clientv3.WatchChan {
		op := clientv3.OpGet(key, opts...)
		if len(op.RangeBytes()) != 0 || op.Rev() != 42 {
			t.Fatal("watch escaped exact key or revision")
		}
		if key != ServicesPrefix+"artifact" && key != ServicesPrefix+"project" {
			t.Fatal("unexpected key")
		}
		ch := make(chan clientv3.WatchResponse, 1)
		streams[key] = ch
		return ch
	}
	changes := registrationChanges(ctx, watch, []ServiceRegistration{{Domain: "artifact"}, {Domain: "project"}}, 41)
	if len(streams) != 2 {
		t.Fatal("missing watcher")
	}
	streams[ServicesPrefix+"artifact"] <- clientv3.WatchResponse{Canceled: true}
	select {
	case response := <-changes:
		if !response.Canceled {
			t.Fatal("lost cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("watch stalled")
	}
	close(streams[ServicesPrefix+"project"])
	select {
	case response := <-changes:
		if !response.Canceled {
			t.Fatal("lost closed watch")
		}
	case <-time.After(time.Second):
		t.Fatal("closed watch stalled")
	}
	cancel()
	select {
	case _, open := <-changes:
		if open {
			t.Fatal("watch leaked after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation stalled")
	}
}
