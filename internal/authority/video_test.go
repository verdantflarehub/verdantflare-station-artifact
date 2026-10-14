package authority

import (
	"context"
	"errors"
	"testing"

	"github.com/verdantflarehub/verdantflare-station-artifact/internal/artifact"
)

func TestVideoNativeWriteUsesProducerAndNeverFallsBack(t *testing.T) {
	p, _, _, _ := routingFixture()
	s := artifact.Source{Kind: "task_output", ServiceID: "video", ProjectID: authorityTestID,
		RunID: "video_task_00000000000000000000000000000001", TaskID: "11111111-1111-4111-8111-111111111111"}
	for _, outcome := range []error{nil, artifact.ErrForbidden, artifact.ErrDependency} {
		baseCalls, producerCalls := 0, 0
		base := authorityFunc(func(context.Context, artifact.Principal, artifact.Permission) error { baseCalls++; return nil })
		producer := authorityFunc(func(_ context.Context, got artifact.Principal, in artifact.Permission) error {
			producerCalls++
			if got != p || *in.Source != s {
				t.Fatal("producer identity changed")
			}
			return outcome
		})
		r, _ := WithVideo(base, producer)
		err := r.Authorize(context.Background(), p, artifact.Permission{Action: "write", Source: &s})
		if !errors.Is(err, outcome) || baseCalls != 0 || producerCalls != 1 {
			t.Fatalf("incorrect producer dispatch: %v, %d, %d", err, baseCalls, producerCalls)
		}
	}
}

func TestVideoNativeRejectsMissingProducerOrAmbiguousScope(t *testing.T) {
	p, _, _, version := routingFixture()
	s := artifact.Source{Kind: "task_output", ServiceID: "video", ProjectID: authorityTestID,
		RunID: "video_task_00000000000000000000000000000001", TaskID: "11111111-1111-4111-8111-111111111111"}
	calls := 0
	base := authorityFunc(func(context.Context, artifact.Principal, artifact.Permission) error { calls++; return nil })
	r, _ := WithVideo(base, nil)
	if !errors.Is(r.Authorize(context.Background(), p, artifact.Permission{Action: "write", Source: &s}), artifact.ErrForbidden) {
		t.Fatal("missing producer allowed")
	}
	r, _ = WithVideo(base, base)
	if !errors.Is(r.Authorize(context.Background(), p, artifact.Permission{Action: "write", Source: &s, Version: &version}), artifact.ErrForbidden) || calls != 0 {
		t.Fatal("ambiguous permission reached authority")
	}
}

func TestVideoNativeReadKeepsProjectAuthority(t *testing.T) {
	p, _, _, version := routingFixture()
	version.Source = artifact.Source{Kind: "task_output", ServiceID: "video", ProjectID: authorityTestID, RunID: "run"}
	baseCalls := 0
	base := authorityFunc(func(context.Context, artifact.Principal, artifact.Permission) error { baseCalls++; return nil })
	producer := authorityFunc(func(context.Context, artifact.Principal, artifact.Permission) error {
		t.Fatal("read reached producer")
		return nil
	})
	r, _ := WithVideo(base, producer)
	if err := r.Authorize(context.Background(), p, artifact.Permission{Action: "read", Version: &version, Access: &artifact.Access{}}); err != nil || baseCalls != 1 {
		t.Fatal("read did not preserve business authority")
	}
}
