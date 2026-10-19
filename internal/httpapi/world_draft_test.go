package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/verdantflarehub/verdantflare-station-artifact/internal/artifact"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestWorldDraftDownloadQueryKeepsFixedScope(t *testing.T) {
	uid := id()
	base := "store_id=" + uid + "&artifact_id=" + uid
	good := base + "&world_draft_id=" + uid + "&world_draft_revision_id=" + uid
	request := func(q string) error {
		r := httptest.NewRequest("GET", "/v2/artifacts/"+uid+"/content?"+q, nil)
		r.SetPathValue("version", uid)
		_, a, err := query(r)
		if err == nil && (a.WorldDraftID != uid || a.WorldDraftRevisionID != uid || a.ProjectID != "") {
			t.Fatal("draft scope lost")
		}
		return err
	}
	if request(good) != nil {
		t.Fatal("valid draft query rejected")
	}
	for _, q := range []string{base + "&world_draft_id=" + uid, good + "&project_id=" + uid + "&project_revision_id=" + uid, good + "&asset_id=" + uid + "&asset_version_id=" + uid, good + "&world_draft_id=" + uid, good + "&world_draft_revision_id="} {
		if request(q) == nil {
			t.Fatal("ambiguous draft query accepted")
		}
	}
}

func TestWorldDraftInternalSourceIsNotPublicMCPWrite(t *testing.T) {
	s := &Server{service: &artifact.Service{}}
	uid := id()
	p := artifact.Principal{OrganizationID: uid, SubjectID: uid, RequestID: uid}
	for _, source := range []artifact.Source{
		{Kind: "world_draft_manifest", WorldDraftID: uid, WorldDraftRevisionID: uid},
		{Kind: "user_import", WorldDraftID: uid, ProjectID: uid},
		{Kind: "user_edit", WorldDraftID: uid, WorldDraftRevisionID: uid},
	} {
		in := map[string]any{"mode": "text", "write_id": uid, "source": source, "mime": "text/plain", "text": "test"}
		raw, _ := json.Marshal(in)
		if _, err := s.writeMCP(context.Background(), p, raw); !errors.Is(err, artifact.ErrInvalid) {
			t.Fatal("unsafe source reached storage")
		}
	}
	if !userSource(artifact.Source{Kind: "user_import", WorldDraftID: uid}) {
		t.Fatal("valid user source rejected before authorization")
	}
}

func TestWorldDraftGeneratedDownloadPathPreservesScope(t *testing.T) {
	uid := id()
	ref := artifact.ContentRef{StoreID: uid, ArtifactID: uid, VersionID: uid}
	access := artifact.Access{WorldDraftID: uid, WorldDraftRevisionID: uid}
	path := downloadContentPath(ref, access)
	u, err := url.Parse(path)
	if err != nil || u.IsAbs() || u.Host != "" {
		t.Fatal("download escaped controlled relative path")
	}
	request := httptest.NewRequest("GET", path, nil)
	request.SetPathValue("version", uid)
	gotRef, gotAccess, err := query(request)
	if err != nil || gotRef != ref || gotAccess != access {
		t.Fatal("generated download discarded or changed the fixed draft scope")
	}
}
