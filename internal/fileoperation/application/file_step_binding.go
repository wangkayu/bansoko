package application

import (
	protocol "bansoko/internal/acquisition/terminal/domain"
	"bytes"
	"fmt"
)

// verifyPublicationStepBinding repeats object relationships independently of
// the SQL recorder; canonical byte hashes alone do not establish provenance.
// Path-level verification: a published object is the regular file
// at the expected location with the source's size. Device/inode are compared
// only between observations of the same invocation (the reserved placeholder
// and the object the rename displaced).
func verifyPublicationStepBinding(step, collision, dependency string, objects, checkpoint, proof protocol.Value) error {
	equal := func(a, b protocol.Value) bool { return bytes.Equal(protocol.Canonical(a), protocol.Canonical(b)) }
	fail := func() error { return fmt.Errorf("FILE_STEP_OBJECT_BINDING_INVALID: %s", step) }
	switch step {
	case "stage_temp":
		if !samePublicationSize(proof.Object["object"], objects.Object["source"]) {
			return fail()
		}
		if proof.Object["kind"].String != "created_temp" || !equal(proof.Object["object"].Object["location"], objects.Object["temp"]) || !equal(proof.Object["parent"], objects.Object["target_parent"]) || proof.Object["verified_source_digest"].String != protocol.Digest("file-source-evidence:v1", protocol.Canonical(objects.Object["source"])) || !proof.Object["flushed"].Bool {
			return fail()
		}
	case "reserve_version":
		if collision != "version" || proof.Object["kind"].String != "reserved_path" || proof.Object["base_path"].String != objects.Object["target"].Object["canonical_path"].String || !equal(proof.Object["parent"], objects.Object["target_parent"]) || proof.Object["object"].Object["size"].Int != 0 {
			return fail()
		}
	case "publish_target":
		if !samePublicationSize(proof.Object["target"], objects.Object["source"]) {
			return fail()
		}
		if collision == "block" && (proof.Object["atomic_method"].String != "no_replace" || proof.Object["displaced"].Object["state"].String != "absent") {
			return fail()
		}
		target := objects.Object["target"]
		if collision == "version" {
			target = checkpoint.Object["reserve_version"].Object["proof"].Object["object"].Object["location"]
		}
		if proof.Object["kind"].String != "published" || !equal(proof.Object["target"].Object["location"], target) || !equal(proof.Object["parent"], objects.Object["target_parent"]) || proof.Object["dependency_digest"].String != dependency {
			return fail()
		}
		displaced := proof.Object["displaced"].Object["value"]
		if collision == "version" || collision == "overwrite" {
			if proof.Object["atomic_method"].String != "replace" || proof.Object["displaced"].Object["state"].String != "known" || !equal(displaced.Object["location"], target) {
				return fail()
			}
		}
		if collision == "version" {
			reserved := checkpoint.Object["reserve_version"].Object["proof"].Object["object"]
			if !samePublicationSize(displaced, reserved) || !equal(displaced.Object["identity"], reserved.Object["identity"]) {
				return fail()
			}
		}
		if collision == "overwrite" && !samePublicationSize(displaced, objects.Object["expected_target"]) {
			return fail()
		}
	case "source_cleanup", "temp_cleanup":
		before := objects.Object["source"]
		parent := objects.Object["source_parent"]
		if step == "temp_cleanup" {
			parent = objects.Object["target_parent"]
			before = checkpoint.Object["stage_temp"].Object["proof"].Object["object"]
		}
		if proof.Object["kind"].String != "removed_object" || !equal(proof.Object["before"], before) || !equal(proof.Object["parent"], parent) || !equal(proof.Object["after"].Object["parent"], parent) || !equal(proof.Object["after"].Object["location"], before.Object["location"]) {
			return fail()
		}
	default:
		return fail()
	}
	return nil
}

// samePublicationSize reports whether two PathObjectV1 values are regular
// files of the same size. Path + size is identity.
func samePublicationSize(actual, expected protocol.Value) bool {
	if actual.Kind != protocol.KindObject || expected.Kind != protocol.KindObject {
		return false
	}
	return actual.Object["file_type"].String == "regular" && expected.Object["file_type"].String == "regular" && actual.Object["size"].Kind == protocol.KindInteger && bytes.Equal(protocol.Canonical(actual.Object["size"]), protocol.Canonical(expected.Object["size"]))
}
