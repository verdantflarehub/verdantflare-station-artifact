package authority

import (
	"context"
	"errors"
	"testing"

	"github.com/verdantflarehub/verdantflare-station-artifact/internal/artifact"
)

func TestSpeechNativeWriteUsesProducerAndNeverFallsBack(t *testing.T) {
	p, _, _, _ := routingFixture()
	s := artifact.Source{Kind: "task_output", ServiceID: "speech", ProjectID: authorityTestID,
		RunID: "01a139c9-f2b8-78c9-92dd-8c71579ef706", TaskID: "01a139c9-f2b8-78c9-92dd-8c71579ef706"}
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
		r, _ := WithSpeech(base, producer)
		err := r.Authorize(context.Background(), p, artifact.Permission{Action: "write", Source: &s})
		if !errors.Is(err, outcome) || baseCalls != 0 || producerCalls != 1 {
			t.Fatalf("incorrect producer dispatch: %v, %d, %d", err, baseCalls, producerCalls)
		}
	}
}

func TestSpeechNativeRejectsMissingProducerOrAmbiguousScope(t *testing.T) {
	p, _, _, version := routingFixture()
	s := artifact.Source{Kind: "task_output", ServiceID: "speech", ProjectID: authorityTestID,
		RunID: "01a139c9-f2b8-78c9-92dd-8c71579ef706", TaskID: "01a139c9-f2b8-78c9-92dd-8c71579ef706"}
	calls := 0
	base := authorityFunc(func(context.Context, artifact.Principal, artifact.Permission) error { calls++; return nil })
	r, _ := WithSpeech(base, nil)
	if !errors.Is(r.Authorize(context.Background(), p, artifact.Permission{Action: "write", Source: &s}), artifact.ErrForbidden) {
		t.Fatal("missing producer allowed")
	}
	r, _ = WithSpeech(base, base)
	if !errors.Is(r.Authorize(context.Background(), p, artifact.Permission{Action: "write", Source: &s, Version: &version}), artifact.ErrForbidden) || calls != 0 {
		t.Fatal("ambiguous permission reached authority")
	}
}

func TestSpeechNativeReadKeepsProjectAuthority(t *testing.T) {
	p, _, _, version := routingFixture()
	version.Source = artifact.Source{Kind: "task_output", ServiceID: "speech", ProjectID: authorityTestID, RunID: "run"}
	baseCalls := 0
	base := authorityFunc(func(context.Context, artifact.Principal, artifact.Permission) error { baseCalls++; return nil })
	producer := authorityFunc(func(context.Context, artifact.Principal, artifact.Permission) error {
		t.Fatal("read reached producer")
		return nil
	})
	r, _ := WithSpeech(base, producer)
	if err := r.Authorize(context.Background(), p, artifact.Permission{Action: "read", Version: &version, Access: &artifact.Access{}}); err != nil || baseCalls != 1 {
		t.Fatal("read did not preserve business authority")
	}
}
