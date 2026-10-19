package artifact

import "testing"

func TestWorldDraftSourceCannotCrossOrigins(t *testing.T) {
	id, other, revision := testID(t), testID(t), testID(t)
	imported := Source{Kind: "user_import", WorldDraftID: id}
	edited := Source{Kind: "user_edit", WorldDraftID: id}
	manifest := Source{Kind: "world_draft_manifest", WorldDraftID: id, WorldDraftRevisionID: revision}
	for _, s := range []Source{imported, edited, manifest} {
		if !s.Valid() || !s.SameOrigin(s) {
			t.Fatal("valid draft source rejected")
		}
	}
	if !imported.SameOrigin(edited) {
		t.Fatal("editing the same draft changed origin")
	}
	for _, s := range []Source{
		{Kind: "user_edit", ProjectID: id},
		{Kind: "user_edit", WorldDraftID: other}, manifest,
		{Kind: "user_import", ProjectID: id, WorldDraftID: id},
	} {
		if imported.SameOrigin(s) || s.SameOrigin(imported) {
			t.Fatal("origin switched across scopes")
		}
	}
	changed := manifest
	changed.WorldDraftRevisionID = other
	if manifest.SameOrigin(changed) {
		t.Fatal("manifest changed its fixed revision")
	}
	for _, s := range []Source{
		{Kind: "user_import", WorldDraftID: id, WorldDraftRevisionID: revision},
		{Kind: "user_edit", WorldDraftID: id, ProjectID: id},
		{Kind: "user_edit", WorldDraftID: id, AssetID: id},
		{Kind: "task_output", WorldDraftID: id, ServiceID: "video", RunID: "native"},
		{Kind: "legacy_import", WorldDraftID: id},
		{Kind: "asset_manifest", WorldDraftID: id, AssetID: id, AssetVersionID: revision},
		{Kind: "world_draft_manifest", WorldDraftID: id},
		{Kind: "world_draft_manifest", WorldDraftID: id, WorldDraftRevisionID: revision, ProjectID: id},
		{Kind: "world_draft_manifest", WorldDraftID: id, WorldDraftRevisionID: revision, RunID: "native"},
		{Kind: "user_edit", WorldDraftID: "not-an-id"},
	} {
		if s.Valid() || s.SameOrigin(s) {
			t.Fatal("invalid source accepted")
		}
	}
}

func TestWorldDraftAccessRequiresOneCompleteScope(t *testing.T) {
	id := testID(t)
	for _, a := range []Access{{}, {ProjectID: id, RevisionID: id}, {AssetID: id, AssetVersionID: id}, {WorldDraftID: id, WorldDraftRevisionID: id}} {
		if !a.Valid() {
			t.Fatal("valid scope rejected")
		}
	}
	for _, a := range []Access{
		{WorldDraftID: id}, {WorldDraftRevisionID: id},
		{WorldDraftID: id, WorldDraftRevisionID: "latest"},
		{WorldDraftID: id, WorldDraftRevisionID: id, ProjectID: id, RevisionID: id},
		{WorldDraftID: id, WorldDraftRevisionID: id, AssetID: id, AssetVersionID: id},
		{WorldDraftID: id, AssetVersionID: id},
	} {
		if a.Valid() {
			t.Fatal("ambiguous or incomplete scope accepted")
		}
	}
	if !(Owner{Kind: "world_draft_revision", ID: id, CommitID: id}).Valid() {
		t.Fatal("draft retention owner rejected")
	}
}
