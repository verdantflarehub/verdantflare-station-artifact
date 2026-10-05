package artifact

import "testing"

func TestAssetManifestSourceBoundary(t *testing.T) {
	source := Source{Kind: "asset_manifest", AssetID: testID(t), AssetVersionID: testID(t)}
	if !source.Valid() || !source.SameOrigin(source) {
		t.Fatal("valid asset manifest scope rejected")
	}
	for _, change := range []func(*Source){func(s *Source) { s.ProjectID = testID(t) }, func(s *Source) { s.RunID = "native-run" }, func(s *Source) { s.AssetVersionID = "" }, func(s *Source) { s.Kind = "user_edit"; s.ProjectID = testID(t) }} {
		bad := source
		change(&bad)
		if bad.Valid() {
			t.Fatal("mixed/missing source scope accepted")
		}
	}
	other := source
	other.AssetVersionID = testID(t)
	if source.SameOrigin(other) {
		t.Fatal("manifest append changed fixed asset version")
	}
}
