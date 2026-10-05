package registry

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// Publication owns only its own lease. A replaced key attached to a different
// instance's lease cannot be removed by this instance's shutdown.
type Publication struct {
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func (p *Publication) Close() { p.once.Do(func() { p.cancel(); <-p.done }) }

func Register(ctx context.Context, cli *clientv3.Client, registrations []ServiceRegistration) (*Publication, error) {
	if cli == nil || len(registrations) == 0 {
		return nil, errors.New("registry configuration required")
	}
	seen := map[string]bool{}
	for _, r := range registrations {
		r.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if !validRegistration(ServicesPrefix+r.Domain, r) || seen[r.Domain] {
			return nil, errors.New("invalid service registration")
		}
		seen[r.Domain] = true
	}
	worker, cancel := context.WithCancel(ctx)
	lease, stream, revision, e := publish(worker, cli, registrations)
	if e != nil {
		cancel()
		return nil, e
	}
	p := &Publication{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(p.done)
		defer func() {
			cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			_, _ = cli.Revoke(cleanup, lease)
		}()
		for {
			watchCtx, stopWatch := context.WithCancel(worker)
			changes := cli.Watch(watchCtx, ServicesPrefix, clientv3.WithPrefix(), clientv3.WithRev(revision+1))
			lost := false
			for !lost {
				select {
				case <-worker.Done():
					stopWatch()
					return
				case status, ok := <-stream:
					lost = !ok || status == nil || status.TTL <= 0
				case response, ok := <-changes:
					lost = !ok || response.Err() != nil || response.Canceled
					for _, event := range response.Events {
						for _, reg := range registrations {
							if event.Type == clientv3.EventTypeDelete && string(event.Kv.Key) == ServicesPrefix+reg.Domain {
								lost = true
							}
						}
					}
				}
			}
			stopWatch()
			cleanup, stopCleanup := context.WithTimeout(context.Background(), 5*time.Second)
			_, _ = cli.Revoke(cleanup, lease)
			stopCleanup()
			for worker.Err() == nil {
				select {
				case <-worker.Done():
					return
				case <-time.After(5 * time.Second):
				}
				newLease, newStream, newRevision, err := publish(worker, cli, registrations)
				if err == nil {
					lease, stream, revision = newLease, newStream, newRevision
					break
				}
			}
		}
	}()
	return p, nil
}
func publish(ctx context.Context, cli *clientv3.Client, registrations []ServiceRegistration) (clientv3.LeaseID, <-chan *clientv3.LeaseKeepAliveResponse, int64, error) {
	op, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	lease, e := cli.Grant(op, 300)
	if e != nil {
		return 0, nil, 0, errors.New("registry lease unavailable")
	}
	ok := false
	defer func() {
		if !ok {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = cli.Revoke(cleanup, lease.ID)
		}
	}()
	checks := []clientv3.Cmp{}
	writes := []clientv3.Op{}
	for _, reg := range registrations {
		key := ServicesPrefix + reg.Domain
		current, e := cli.Get(op, key)
		if e != nil {
			return 0, nil, 0, errors.New("registry read unavailable")
		}
		if len(current.Kvs) > 0 {
			var prior ServiceRegistration
			if json.Unmarshal(current.Kvs[0].Value, &prior) != nil || prior.Endpoint != reg.Endpoint {
				return 0, nil, 0, errors.New("domain already registered at another endpoint")
			}
			checks = append(checks, clientv3.Compare(clientv3.ModRevision(key), "=", current.Kvs[0].ModRevision))
		} else {
			checks = append(checks, clientv3.Compare(clientv3.Version(key), "=", 0))
		}
		reg.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		data, e := json.Marshal(reg)
		if e != nil {
			return 0, nil, 0, errors.New("invalid service registration")
		}
		writes = append(writes, clientv3.OpPut(key, string(data), clientv3.WithLease(lease.ID)))
	}
	result, e := cli.Txn(op).If(checks...).Then(writes...).Commit()
	if e != nil || !result.Succeeded {
		return 0, nil, 0, errors.New("registration changed concurrently")
	}
	stream, e := cli.KeepAlive(ctx, lease.ID)
	if e != nil {
		return 0, nil, 0, errors.New("registry keepalive unavailable")
	}
	ok = true
	return lease.ID, stream, result.Header.Revision, nil
}
