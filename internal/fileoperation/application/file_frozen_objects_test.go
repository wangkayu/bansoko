package application

import (
	protocol "bansoko/internal/acquisition/terminal/domain"
	"bansoko/internal/library/domain"
	"testing"
	"time"
)

func TestFrozenPublicationObjectsRejectIncompleteEvidence(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	observation := domain.PathObservation{SizeBytes: 8, Identity: domain.FileIdentity{Device: 1, Inode: 2}, ObservedAt: now}
	op := domain.FileOperation{Operation: domain.OperationHardlink, CollisionPolicy: domain.CollisionOverwrite, SourcePath: "/source/file", TargetPath: "/target/file"}
	prep := domain.PublicationPreparation{Source: observation, ExpectedTarget: &observation, SourceParent: observation.Identity, TargetParent: observation.Identity, TempPath: "/target/temp"}
	raw, err := preparedPublicationObjects(op, prep)
	if err != nil {
		t.Fatal(err)
	}
	clean, err := protocol.ParseStrict([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePublicationObjects(clean, "hardlink", "overwrite"); err != nil {
		t.Fatalf("valid frozen evidence refused: %v", err)
	}
	for _, kind := range []string{"missing_target", "extra", "bad_source", "content_proof", "missing_identity", "negative_size", "non_regular"} {
		t.Run(kind, func(t *testing.T) {
			objects, err := protocol.ParseStrict([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			target := objects.Object["expected_target"]
			switch kind {
			case "missing_target":
				delete(objects.Object, "expected_target")
			case "extra":
				objects.Object["foreign"] = protocol.Value{Kind: protocol.KindString, String: "untrusted"}
			case "bad_source":
				objects.Object["source"] = protocol.Value{Kind: protocol.KindObject, Object: map[string]protocol.Value{}}
			case "content_proof":
				v, _ := protocol.ParseStrict([]byte(`{"state":"known","value":{"kind":"sha256_file"},"reason_code":null}`))
				target.Object["content"] = v
			case "missing_identity":
				delete(target.Object, "identity")
			case "negative_size":
				target.Object["size"] = protocol.Value{Kind: protocol.KindInteger, Int: -1}
			case "non_regular":
				target.Object["file_type"] = protocol.Value{Kind: protocol.KindString, String: "directory"}
			}
			if kind != "missing_target" && kind != "extra" && kind != "bad_source" {
				if err := validatePublicationObservation(target, "target"); err == nil {
					t.Fatal("independent object consumer accepted invalid observation")
				}
			}
			if err := validatePublicationObjects(objects, "hardlink", "overwrite"); err == nil {
				t.Fatal("incomplete frozen evidence accepted")
			}
		})
	}
}
