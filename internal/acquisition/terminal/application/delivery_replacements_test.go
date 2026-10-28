package application

import (
	"strings"
	"testing"
	"time"

	protocol "bansoko/internal/acquisition/terminal/domain"
	library "bansoko/internal/library/domain"
	"bansoko/internal/testutil/nativeinput"
)

func approvalFixture(t *testing.T, path string) protocol.Value {
	t.Helper()
	at := time.Unix(1700000000, 0).UTC()
	observation := library.PathObservation{SizeBytes: 10, Identity: library.FileIdentity{Device: 1, Inode: 2}, ObservedAt: at}
	size := int64(10)
	comparison := map[string]any{"policy": map[string]any{"ref": "anime-1", "version": 1}, "candidate_quality": replacementQuality(replacementFactsSnapshot{Resolution: "1080p", Size: &size}), "existing_quality": replacementQuality(replacementFactsSnapshot{Resolution: "720p", Size: &size}), "compared_dimensions": []string{"resolution"}, "verdict": "replace", "evaluated_at": protocol.FormatTime(at)}
	value, err := valueOf(map[string]any{"placement": map[string]any{"ref": "placement-" + path, "version": 1}, "expected_object": replacementObject(path, observation), "comparison": comparison})
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.Validate("ReplacementV1", value); err != nil {
		t.Fatal(err)
	}
	return value
}

// The delivery replacements list (input schema 1) carries one approval per
// present approved target, sorted and unique by target path. Approvals mean
// intent replace under any global collision (an approved replacement takes
// effect above skip and version), and intent replace needs at least one.
func TestDeliveryReplacementsApprovalList(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	input, err := protocol.ParseStrict([]byte(nativeinput.Canonical(t, digest, digest)))
	if err != nil {
		t.Fatal(err)
	}
	base := input.Object["delivery"]
	delivery := func(collision, intent string, paths ...string) protocol.Value {
		next := protocol.Value{Kind: protocol.KindObject, Object: map[string]protocol.Value{}}
		for key, value := range base.Object {
			next.Object[key] = value
		}
		next.Object["collision"] = protocol.Value{Kind: protocol.KindString, String: collision}
		next.Object["intent"] = protocol.Value{Kind: protocol.KindString, String: intent}
		list := protocol.Value{Kind: protocol.KindArray}
		for _, path := range paths {
			list.Array = append(list.Array, approvalFixture(t, path))
		}
		next.Object["replacements"] = list
		return next
	}
	for name, c := range map[string]struct {
		value protocol.Value
		ok    bool
	}{
		"two approvals":             {delivery("overwrite", "replace", "/library/a.mkv", "/library/b.mkv"), true},
		"one approval":              {delivery("overwrite", "replace", "/library/a.mkv"), true},
		"no overwrite":              {delivery("block", "ensure_present"), true},
		"unsorted":                  {delivery("overwrite", "replace", "/library/b.mkv", "/library/a.mkv"), false},
		"duplicate path":            {delivery("overwrite", "replace", "/library/a.mkv", "/library/a.mkv"), false},
		"overwrite, no approval":    {delivery("overwrite", "replace"), false},
		"approval under skip":       {delivery("block", "replace", "/library/a.mkv"), true},
		"approval under version":    {delivery("version", "replace", "/library/a.mkv"), true},
		"skip replace, no approval": {delivery("block", "replace"), false},
		"approval not replace":      {delivery("block", "ensure_present", "/library/a.mkv"), false},
		"overwrite not replace":     {delivery("overwrite", "ensure_present", "/library/a.mkv"), false},
	} {
		err := protocol.Validate("DeliveryV1", c.value)
		if (err == nil) != c.ok {
			t.Fatalf("%s: err = %v, want ok %v", name, err, c.ok)
		}
	}
	two := input
	two.Object["delivery"] = delivery("overwrite", "replace", "/library/a.mkv", "/library/b.mkv")
	if err := protocol.Validate("AcquisitionInputV1", two); err != nil {
		t.Fatalf("input v7 with two approvals: %v", err)
	}
}
