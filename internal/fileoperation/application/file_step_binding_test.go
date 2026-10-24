package application

import (
	protocol "bansoko/internal/acquisition/terminal/domain"
	"testing"
)

// Path-level binding: a published object is the regular file at the expected
// location with the source's size; a foreign size, location or non-regular
// object is refused. No content digest participates.
func TestPublicationStepBindingComparesLocationAndSize(t *testing.T) {
	parse := func(raw string) protocol.Value {
		v, err := protocol.ParseStrict([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	objects := parse(`{"source":{"file_type":"regular","size":8,"location":{"canonical_path":"/src/a"}},"target":{"canonical_path":"/lib/a"},"temp":{"canonical_path":"/lib/.t"},"target_parent":{"kind":"local_fs","device":"1","inode":"9"},"source_parent":{"kind":"local_fs","device":"1","inode":"8"},"expected_target":{"file_type":"regular","size":5,"location":{"canonical_path":"/lib/a"}}}`)
	sourceDigest := protocol.Digest("file-source-evidence:v1", protocol.Canonical(objects.Object["source"]))
	stage := parse(`{"kind":"created_temp","object":{"file_type":"regular","size":8,"location":{"canonical_path":"/lib/.t"},"identity":{"kind":"local_fs","device":"1","inode":"3"}},"parent":{"kind":"local_fs","device":"1","inode":"9"},"verified_source_digest":"` + sourceDigest + `","flushed":true}`)
	if err := verifyPublicationStepBinding("stage_temp", "block", "dep", objects, protocol.Value{}, stage); err != nil {
		t.Fatal(err)
	}
	block := parse(`{"kind":"published","target":{"file_type":"regular","size":8,"location":{"canonical_path":"/lib/a"}},"parent":{"kind":"local_fs","device":"1","inode":"9"},"dependency_digest":"dep","atomic_method":"no_replace","displaced":{"state":"absent","value":null,"reason_code":null}}`)
	if err := verifyPublicationStepBinding("publish_target", "block", "dep", objects, protocol.Value{}, block); err != nil {
		t.Fatal(err)
	}
	overwrite := parse(`{"kind":"published","target":{"file_type":"regular","size":8,"location":{"canonical_path":"/lib/a"}},"parent":{"kind":"local_fs","device":"1","inode":"9"},"dependency_digest":"dep","atomic_method":"replace","displaced":{"state":"known","value":{"file_type":"regular","size":5,"location":{"canonical_path":"/lib/a"},"identity":{"kind":"local_fs","device":"1","inode":"77"}},"reason_code":null}}`)
	if err := verifyPublicationStepBinding("publish_target", "overwrite", "dep", objects, protocol.Value{}, overwrite); err != nil {
		t.Fatalf("overwrite with displaced object of the expected size refused: %v", err)
	}
	checkpoint := parse(`{"reserve_version":{"proof":{"object":{"file_type":"regular","size":0,"location":{"canonical_path":"/lib/a (1)"},"identity":{"kind":"local_fs","device":"1","inode":"5"}}}}}`)
	version := parse(`{"kind":"published","target":{"file_type":"regular","size":8,"location":{"canonical_path":"/lib/a (1)"}},"parent":{"kind":"local_fs","device":"1","inode":"9"},"dependency_digest":"dep","atomic_method":"replace","displaced":{"state":"known","value":{"file_type":"regular","size":0,"location":{"canonical_path":"/lib/a (1)"},"identity":{"kind":"local_fs","device":"1","inode":"5"}},"reason_code":null}}`)
	if err := verifyPublicationStepBinding("publish_target", "version", "dep", objects, checkpoint, version); err != nil {
		t.Fatalf("version replacing its own placeholder refused: %v", err)
	}
	cases := map[string]struct {
		step, collision string
		proof           protocol.Value
		edit            func(protocol.Value)
		checkpoint      protocol.Value
	}{
		"stage size":           {"stage_temp", "block", stage, func(v protocol.Value) { v.Object["object"].Object["size"] = parse(`9`) }, protocol.Value{}},
		"stage location":       {"stage_temp", "block", stage, func(v protocol.Value) { v.Object["object"].Object["location"] = parse(`{"canonical_path":"/x"}`) }, protocol.Value{}},
		"target size":          {"publish_target", "block", block, func(v protocol.Value) { v.Object["target"].Object["size"] = parse(`9`) }, protocol.Value{}},
		"target non regular":   {"publish_target", "block", block, func(v protocol.Value) { v.Object["target"].Object["file_type"] = parse(`"directory"`) }, protocol.Value{}},
		"block displaced":      {"publish_target", "block", block, func(v protocol.Value) { v.Object["displaced"] = parse(`{"state":"known","value":{}}`) }, protocol.Value{}},
		"block replace":        {"publish_target", "block", block, func(v protocol.Value) { v.Object["atomic_method"] = parse(`"replace"`) }, protocol.Value{}},
		"overwrite no_replace": {"publish_target", "overwrite", overwrite, func(v protocol.Value) { v.Object["atomic_method"] = parse(`"no_replace"`) }, protocol.Value{}},
		"overwrite foreign":    {"publish_target", "overwrite", overwrite, func(v protocol.Value) { v.Object["displaced"].Object["value"].Object["size"] = parse(`6`) }, protocol.Value{}},
		"overwrite elsewhere": {"publish_target", "overwrite", overwrite, func(v protocol.Value) {
			v.Object["displaced"].Object["value"].Object["location"] = parse(`{"canonical_path":"/lib/.t"}`)
		}, protocol.Value{}},
		"version other inode": {"publish_target", "version", version, func(v protocol.Value) {
			v.Object["displaced"].Object["value"].Object["identity"] = parse(`{"kind":"local_fs","device":"1","inode":"6"}`)
		}, checkpoint},
		"version non-empty":    {"publish_target", "version", version, func(v protocol.Value) { v.Object["displaced"].Object["value"].Object["size"] = parse(`3`) }, checkpoint},
		"version wrong target": {"publish_target", "version", version, func(v protocol.Value) { v.Object["target"].Object["location"] = parse(`{"canonical_path":"/lib/a"}`) }, checkpoint},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			candidate := parse(string(protocol.Canonical(tc.proof)))
			tc.edit(candidate)
			if err := verifyPublicationStepBinding(tc.step, tc.collision, "dep", objects, tc.checkpoint, candidate); err == nil {
				t.Fatal("foreign publication object accepted")
			}
		})
	}
}
