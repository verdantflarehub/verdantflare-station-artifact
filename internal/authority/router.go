package authority

import (
	"context"
	"errors"

	"github.com/verdantflarehub/verdantflare-station-artifact/internal/artifact"
)

// Router selects exactly one authority from the trusted permission's operation
// and grant scope. A denial or outage never triggers a second authority.
type Router struct{ project, world artifact.Authorizer }

func NewRouter(project, world artifact.Authorizer) (*Router, error) {
	if project == nil || world == nil {
		return nil, errors.New("both business authorities are required")
	}
	return &Router{project: project, world: world}, nil
}

func (r *Router) Authorize(ctx context.Context, p artifact.Principal, permission artifact.Permission) error {
	if !p.Valid() {
		return artifact.ErrForbidden
	}
	domain, ok := authorityDomain(p, permission)
	if !ok {
		return artifact.ErrForbidden
	}
	if ctx.Err() != nil {
		return artifact.ErrDependency
	}
	if domain == "world" {
		return r.world.Authorize(ctx, p, permission)
	}
	return r.project.Authorize(ctx, p, permission)
}

func authorityDomain(p artifact.Principal, in artifact.Permission) (string, bool) {
	validVersion := func() bool {
		return in.Version != nil && in.Version.ContentRef.Valid() && in.Version.OrganizationID == p.OrganizationID && in.Version.Source.Valid()
	}
	switch in.Action {
	case "write":
		if in.Source == nil || !in.Source.Valid() || in.Version != nil || in.Owner != nil || in.Access != nil {
			return "", false
		}
		if in.Source.Kind == "asset_manifest" {
			return "world", true
		}
		switch in.Source.Kind {
		case "user_edit", "user_import", "legacy_import":
			return "project", true
		default:
			return "", false
		}
	case "read", "delete":
		if !validVersion() || in.Access == nil || !in.Access.Valid() || in.Source != nil || in.Owner != nil {
			return "", false
		}
		a := *in.Access
		if in.Action == "delete" && a != (artifact.Access{}) {
			return "", false
		}
		if a.AssetID != "" {
			return "world", true
		}
		if a.ProjectID != "" {
			return "project", true
		}
		// Without an explicit published-reference scope, the immutable source
		// owns the decision. The service may still deny unsupported direct reads.
		if in.Version.Source.Kind == "asset_manifest" {
			return "world", true
		}
		if artifact.ValidID(in.Version.Source.ProjectID) {
			return "project", true
		}
		return "", false
	case "retain", "retain_content", "inspect_retention", "release":
		if in.Owner == nil || !in.Owner.Valid() || in.Source != nil || in.Access != nil {
			return "", false
		}
		if in.Action == "retain_content" {
			if !validVersion() {
				return "", false
			}
		} else if in.Version != nil {
			return "", false
		}
		// Source and owner may intentionally differ: importing a fixed asset
		// into a Project creates Project retention. The selected owner service
		// must verify the exact immutable plan AND its live cross-domain grant.
		switch in.Owner.Kind {
		case "project_revision":
			return "project", true
		case "asset_version":
			return "world", true
		default:
			return "", false
		}
	}
	return "", false
}
