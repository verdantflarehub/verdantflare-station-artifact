package authority

import (
	"context"
	"errors"
	"github.com/verdantflarehub/verdantflare-station-artifact/internal/artifact"
	"testing"
)

func TestWorldDraftRoutingUsesOnlyWorld(t *testing.T) {
	p, projectSource, _, version := routingFixture()
	id := authorityTestID
	source := artifact.Source{Kind: "user_import", WorldDraftID: id}
	manifest := artifact.Source{Kind: "world_draft_manifest", WorldDraftID: id, WorldDraftRevisionID: id}
	draftVersion := version
	draftVersion.Source = source
	access := artifact.Access{WorldDraftID: id, WorldDraftRevisionID: id}
	empty := artifact.Access{}
	owner := artifact.Owner{Kind: "world_draft_revision", ID: id, CommitID: id}
	for _, permission := range []artifact.Permission{
		{Action: "write", Source: &source}, {Action: "write", Source: &manifest},
		{Action: "read", Version: &draftVersion, Access: &empty},
		{Action: "read", Version: &version, Access: &access},
		{Action: "delete", Version: &draftVersion, Access: &empty},
		{Action: "retain", Owner: &owner}, {Action: "retain_content", Owner: &owner, Version: &version},
		{Action: "inspect_retention", Owner: &owner}, {Action: "release", Owner: &owner},
	} {
		for _, outcome := range []error{nil, artifact.ErrForbidden, artifact.ErrDependency} {
			calls := 0
			r, _ := NewRouter(authorityFunc(func(context.Context, artifact.Principal, artifact.Permission) error {
				t.Fatal("World decision fell back to Project")
				return nil
			}), authorityFunc(func(_ context.Context, got artifact.Principal, in artifact.Permission) error {
				calls++
				if got != p || in.Action != permission.Action {
					t.Fatal("request identity changed")
				}
				return outcome
			}))
			err := r.Authorize(context.Background(), p, permission)
			if !errors.Is(err, outcome) || calls != 1 {
				t.Fatal("World decision changed")
			}
		}
	}
	// An explicit project revision still owns the decision for draft-origin bytes.
	pa := artifact.Access{ProjectID: id, RevisionID: id}
	calls := 0
	r, _ := NewRouter(authorityFunc(func(context.Context, artifact.Principal, artifact.Permission) error { calls++; return nil }), authorityFunc(func(context.Context, artifact.Principal, artifact.Permission) error {
		t.Fatal("wrong authority")
		return nil
	}))
	if err := r.Authorize(context.Background(), p, artifact.Permission{Action: "read", Version: &draftVersion, Access: &pa}); err != nil || calls != 1 {
		t.Fatal("explicit Project scope lost")
	}
	// A mixed source never reaches either authority.
	projectSource.WorldDraftID = id
	calls = 0
	if !errors.Is(r.Authorize(context.Background(), p, artifact.Permission{Action: "write", Source: &projectSource}), artifact.ErrForbidden) || calls != 0 {
		t.Fatal("mixed source reached authority")
	}
}
