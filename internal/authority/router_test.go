package authority

import (
	"context"
	"errors"
	"testing"

	"github.com/verdantflarehub/verdantflare-station-artifact/internal/artifact"
)

type authorityFunc func(context.Context, artifact.Principal, artifact.Permission) error

func (f authorityFunc) Authorize(ctx context.Context, p artifact.Principal, in artifact.Permission) error {
	return f(ctx, p, in)
}

const authorityTestID = "019a00b0-0000-7000-8000-000000000001"

func routingFixture() (artifact.Principal, artifact.Source, artifact.Source, artifact.Version) {
	id := authorityTestID
	p := artifact.Principal{OrganizationID: id, SubjectID: id, RequestID: id}
	project := artifact.Source{Kind: "user_edit", ProjectID: id}
	world := artifact.Source{Kind: "asset_manifest", AssetID: id, AssetVersionID: id}
	v := artifact.Version{ContentRef: artifact.ContentRef{StoreID: id, ArtifactID: id, VersionID: id}, OrganizationID: id, Source: project}
	return p, project, world, v
}

func TestAuthorityRoutingUsesOneExplicitDomain(t *testing.T) {
	p, projectSource, worldSource, v := routingFixture()
	id := authorityTestID
	worldVersion := v
	worldVersion.Source = worldSource
	projectAccess := artifact.Access{ProjectID: id, RevisionID: id}
	worldAccess := artifact.Access{AssetID: id, AssetVersionID: id}
	empty := artifact.Access{}
	projectOwner := artifact.Owner{Kind: "project_revision", ID: id, CommitID: id}
	worldOwner := artifact.Owner{Kind: "asset_version", ID: id, CommitID: id}
	for _, tc := range []struct {
		name, domain string
		permission   artifact.Permission
	}{
		{"project_write", "project", artifact.Permission{Action: "write", Source: &projectSource}},
		{"world_write", "world", artifact.Permission{Action: "write", Source: &worldSource}},
		{"project_source_read", "project", artifact.Permission{Action: "read", Version: &v, Access: &empty}},
		{"world_source_read", "world", artifact.Permission{Action: "read", Version: &worldVersion, Access: &empty}},
		{"world_read_of_project_content", "world", artifact.Permission{Action: "read", Version: &v, Access: &worldAccess}},
		{"project_read_of_world_content", "project", artifact.Permission{Action: "read", Version: &worldVersion, Access: &projectAccess}},
		{"project_delete", "project", artifact.Permission{Action: "delete", Version: &v, Access: &empty}},
		{"world_delete", "world", artifact.Permission{Action: "delete", Version: &worldVersion, Access: &empty}},
		{"project_retains_world_content", "project", artifact.Permission{Action: "retain_content", Owner: &projectOwner, Version: &worldVersion}},
		{"world_retains_project_content", "world", artifact.Permission{Action: "retain_content", Owner: &worldOwner, Version: &v}},
		{"world_release", "world", artifact.Permission{Action: "release", Owner: &worldOwner}},
		{"project_inspect", "project", artifact.Permission{Action: "inspect_retention", Owner: &projectOwner}},
		{"project_retain", "project", artifact.Permission{Action: "retain", Owner: &projectOwner}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := []string{}
			makeAuthority := func(domain string) artifact.Authorizer {
				return authorityFunc(func(_ context.Context, got artifact.Principal, in artifact.Permission) error {
					calls = append(calls, domain)
					if got != p || in.Action != tc.permission.Action {
						t.Fatal("principal or operation changed")
					}
					return nil
				})
			}
			r, _ := NewRouter(makeAuthority("project"), makeAuthority("world"))
			if err := r.Authorize(context.Background(), p, tc.permission); err != nil {
				t.Fatal(err)
			}
			if len(calls) != 1 || calls[0] != tc.domain {
				t.Fatal(calls)
			}
		})
	}
}

func TestAuthorityRoutingRejectsAmbiguousOrMalformedScope(t *testing.T) {
	p, source, world, v := routingFixture()
	id := authorityTestID
	empty := artifact.Access{}
	mixed := artifact.Access{ProjectID: id, RevisionID: id, AssetID: id, AssetVersionID: id}
	partial := artifact.Access{AssetID: id}
	owner := artifact.Owner{Kind: "pending_commit", ID: id, CommitID: id}
	projectOwner := artifact.Owner{Kind: "project_revision", ID: id, CommitID: id}
	badSource := world
	badSource.ProjectID = id
	foreign := v
	foreign.OrganizationID = "019a00b0-0000-7000-8000-000000000002"
	for _, in := range []artifact.Permission{
		{Action: "read", Version: &v, Access: &mixed}, {Action: "read", Version: &v, Access: &partial}, {Action: "read", Version: &foreign, Access: &empty},
		{Action: "read", Version: &v}, {Action: "delete", Version: &v, Access: &partial}, {Action: "write", Source: &badSource}, {Action: "write", Source: &source, Access: &empty},
		{Action: "retain", Owner: &owner}, {Action: "retain", Owner: &projectOwner, Version: &v}, {Action: "retain_content", Owner: &projectOwner},
		{Action: "release", Owner: &projectOwner, Source: &source}, {Action: "unknown", Source: &source},
	} {
		calls := 0
		deny := authorityFunc(func(context.Context, artifact.Principal, artifact.Permission) error { calls++; return nil })
		r, _ := NewRouter(deny, deny)
		if !errors.Is(r.Authorize(context.Background(), p, in), artifact.ErrForbidden) || calls != 0 {
			t.Fatal("malformed scope reached authority", in.Action)
		}
	}
}

func TestAuthorityRoutingNeverRetriesAnotherDomain(t *testing.T) {
	p, source, _, _ := routingFixture()
	for _, failure := range []error{artifact.ErrForbidden, artifact.ErrDependency} {
		calls := 0
		r, _ := NewRouter(authorityFunc(func(context.Context, artifact.Principal, artifact.Permission) error { calls++; return failure }), authorityFunc(func(context.Context, artifact.Principal, artifact.Permission) error {
			t.Fatal("other domain used as fallback")
			return nil
		}))
		if !errors.Is(r.Authorize(context.Background(), p, artifact.Permission{Action: "write", Source: &source}), failure) || calls != 1 {
			t.Fatal("decision changed")
		}
	}
}
