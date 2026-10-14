package authority

import (
	"context"
	"errors"

	"github.com/verdantflarehub/verdantflare-station-artifact/internal/artifact"
)

// SpeechNative delegates only native Speech writes. It cannot grant reads or
// retention, and an absent producer or denied task never falls through.
type SpeechNative struct{ base, speech artifact.Authorizer }

func WithSpeech(base, speech artifact.Authorizer) (*SpeechNative, error) {
	if base == nil {
		return nil, errors.New("business authority required")
	}
	return &SpeechNative{base: base, speech: speech}, nil
}

func (r *SpeechNative) Authorize(ctx context.Context, p artifact.Principal, in artifact.Permission) error {
	if !p.Valid() {
		return artifact.ErrForbidden
	}
	if ctx.Err() != nil {
		return artifact.ErrDependency
	}
	if in.Action == "write" && in.Source != nil && in.Source.Kind == "task_output" && in.Source.ServiceID == "speech" {
		if r.speech == nil || !in.Source.Valid() || !artifact.ValidID(in.Source.ProjectID) ||
			!artifact.ValidID(in.Source.TaskID) || !artifact.ValidID(in.Source.RunID) || in.Version != nil || in.Owner != nil || in.Access != nil {
			return artifact.ErrForbidden
		}
		return r.speech.Authorize(ctx, p, in)
	}
	return r.base.Authorize(ctx, p, in)
}
