package domain

import "testing"

func TestObservedSatisfactionClosedWireAndRelations(t *testing.T) {
	g := loadGolden(t)
	defs := map[string]Value{}
	for name, raw := range g.Definitions {
		v, err := ParseStrict(raw)
		if err != nil {
			t.Fatal(err)
		}
		defs[name] = v
	}
	object := expandFixture(t, defs, defs["object"], []string{"object"})
	evidence := obj(map[string]Value{"schema_version": i64(2), "basis": str("path_size"), "acquisition_id": str("root"), "plan_id": str("plan"), "artifact_id": str("artifact"), "artifact_version": i64(1), "file_ref": str("f"), "input_digest": str("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), "spec_digest": str("sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"), "expected_size": object.Object["size"], "observed_object": object})
	if err := ValidateObservedSatisfaction(evidence); err != nil {
		t.Fatal(err)
	}
	// Independently expanded and SHA256-computed in Python; no production
	// encoder or digest helper participated in this expected value.
	canonical := Canonical(evidence)
	if len(canonical) != 758 || Digest("acquisition-observed-satisfaction:v1", canonical) != "sha256:53ef3b37416bbb3d5f70782030eac9b19b1ec82dc16537f272319abfb917a942" {
		t.Fatalf("golden bytes/hash: %d %s", len(canonical), Digest("acquisition-observed-satisfaction:v1", canonical))
	}
	for path, value := range map[string]Value{
		"/expected_size":                  i64(1),
		"/observed_object/size":           i64(1),
		"/observed_object/identity/inode": str("bad"),
		"/observed_object/file_type":      str("directory"),
		"/basis":                          str("stable_read"),
		"/schema_version":                 i64(1),
	} {
		t.Run(path, func(t *testing.T) {
			bad := with(evidence, path, value)
			if err := ValidateObservedSatisfaction(bad); err == nil {
				t.Fatalf("accepted %s", path)
			}
		})
	}
	for _, key := range []string{"trusted", "expected_content"} {
		bad := clone(evidence)
		bad.Object[key] = Value{Kind: KindBool, Bool: true}
		if err := ValidateObservedSatisfaction(bad); err == nil {
			t.Fatalf("accepted extra field %s", key)
		}
	}
	bad := clone(evidence)
	bad.Object["observed_object"].Object["content"] = Value{Kind: KindNull}
	if err := ValidateObservedSatisfaction(bad); err == nil {
		t.Fatal("accepted a content proof on the observed object")
	}
}
