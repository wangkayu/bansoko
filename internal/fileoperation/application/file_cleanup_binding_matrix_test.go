package application

import (
	protocol "bansoko/internal/acquisition/terminal/domain"
	"testing"
)

func TestPublicationCleanupRelationshipMatrix(t *testing.T) {
	// A displaced object is never a cleanup subject: rename(2) already
	// replaced it at the final location.
	parse := func(raw string) protocol.Value {
		v, e := protocol.ParseStrict([]byte(raw))
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	objects := parse(`{"source":{"location":{"canonical_path":"/source"}},"source_parent":{"inode":"source-parent"},"target_parent":{"inode":"target-parent"}}`)
	checkpoint := parse(`{"stage_temp":{"proof":{"object":{"location":{"canonical_path":"/temp"}}}},"publish_target":{"proof":{"displaced":{"value":{"location":{"canonical_path":"/displaced"}}}}}}`)
	for _, step := range []string{"source_cleanup", "temp_cleanup"} {
		for _, collision := range []string{"block", "version", "overwrite"} {
			t.Run(step+"/"+collision, func(t *testing.T) {
				before, parent := objects.Object["source"], objects.Object["source_parent"]
				if step == "temp_cleanup" {
					parent = objects.Object["target_parent"]
					// The temp cleanup always removes the staged temp object,
					// whatever the collision.
					before = checkpoint.Object["stage_temp"].Object["proof"].Object["object"]
				}
				proof := parse(`{"kind":"removed_object","before":{},"parent":{},"after":{"parent":{},"location":{}}}`)
				proof.Object["before"] = before
				proof.Object["parent"] = parent
				proof.Object["after"].Object["parent"] = parent
				proof.Object["after"].Object["location"] = before.Object["location"]
				if e := verifyPublicationStepBinding(step, collision, "", objects, checkpoint, proof); e != nil {
					t.Fatal(e)
				}
				if step == "temp_cleanup" {
					displaced := parse(string(protocol.Canonical(proof)))
					displaced.Object["before"] = checkpoint.Object["publish_target"].Object["proof"].Object["displaced"].Object["value"]
					displaced.Object["after"].Object["location"] = displaced.Object["before"].Object["location"]
					if e := verifyPublicationStepBinding(step, collision, "", objects, checkpoint, displaced); e == nil {
						t.Fatal("accepted a displaced object as the temp cleanup subject")
					}
				}
				for _, field := range []string{"kind", "before", "parent", "after_parent", "after_location"} {
					bad := parse(string(protocol.Canonical(proof)))
					switch field {
					case "kind":
						bad.Object["kind"] = parse(`"published"`)
					case "before", "parent":
						bad.Object[field] = parse(`{"foreign":true}`)
					case "after_parent":
						bad.Object["after"].Object["parent"] = parse(`{"foreign":true}`)
					case "after_location":
						bad.Object["after"].Object["location"] = parse(`{"foreign":true}`)
					}
					if e := verifyPublicationStepBinding(step, collision, "", objects, checkpoint, bad); e == nil {
						t.Fatalf("accepted foreign %s", field)
					}
				}
			})
		}
	}
}
