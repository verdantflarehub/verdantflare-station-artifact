package authority

import (
	"context"
	"errors"

	"github.com/verdantflarehub/verdantflare-station-artifact/internal/artifact"
)

// VideoNative delegates only native Video writes to their producer. Reads and
// immutable published-reference grants continue to use the business authority.
type VideoNative struct{ base, video artifact.Authorizer }

func WithVideo(base, video artifact.Authorizer) (*VideoNative, error) {
	if base == nil {
		return nil, errors.New("business authority required")
	}
	return &VideoNative{base: base, video: video}, nil
}

func (r *VideoNative) Authorize(ctx context.Context, p artifact.Principal, in artifact.Permission) error {
	if !p.Valid() {
		return artifact.ErrForbidden
	}
	if ctx.Err() != nil {
		return artifact.ErrDependency
	}
	if in.Action == "write" && in.Source != nil && in.Source.Kind == "task_output" && in.Source.ServiceID == "video" {
		if r.video == nil || !in.Source.Valid() || !artifact.ValidID(in.Source.ProjectID) ||
			in.Source.TaskID == "" || in.Version != nil || in.Owner != nil || in.Access != nil {
			return artifact.ErrForbidden
		}
		// A denial or outage must never fall through to another source or authority.
		return r.video.Authorize(ctx, p, in)
	}
	return r.base.Authorize(ctx, p, in)
}
