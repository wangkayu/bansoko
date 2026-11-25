package application

import (
	"context"
	"testing"

	protocol "bansoko/internal/acquisition/terminal/domain"
	anime "bansoko/internal/anime/domain"
	catalog "bansoko/internal/catalog/domain"
	"bansoko/internal/testutil/nativeinput"
)

func serviceIntentFixture(t *testing.T, c admissionCapture) acquisitionIntent {
	t.Helper()
	v, err := protocol.ParseStrict([]byte(nativeinput.Canonical(t, c.payloadDigest, c.configDigest)))
	if err != nil {
		t.Fatal(err)
	}
	// The golden fixture is an older input shape; current intents carry neither
	// the retired source offset nor the duplicate media-quality authority.
	delete(v.Object["media"].Object, "source_offset")
	delete(v.Object["media"].Object, "quality")
	// and the delivery is the replacements shape: the golden fixture carries no
	// approved replacement, so the approval list is empty.
	delete(v.Object["delivery"].Object, "replacement")
	v.Object["delivery"].Object["replacements"] = protocol.Value{Kind: protocol.KindArray}
	if c.material.bgm != nil {
		v.Object["media"].Object["bgm"], err = valueOf(frozenValue("known", map[string]any{"ref": c.material.bgm.WorkId, "version": c.material.metadataVersion}))
		if err != nil {
			t.Fatal(err)
		}
	}
	if c.material.tmdb != nil {
		v.Object["media"].Object["tmdb"], err = valueOf(frozenValue("known", map[string]any{"work_ref": c.material.tmdb.WorkId, "kind": c.material.tmdb.MediaKind, "season": c.material.tmdb.Season, "version": c.material.metadataVersion}))
		if err != nil {
			t.Fatal(err)
		}
	}
	mode := c.material.entryMode
	if mode == "" {
		t.Fatal("fixture capture must specify entry mode")
	}
	return acquisitionIntent{entryMode: mode, title: "Show", media: v.Object["media"], delivery: v.Object["delivery"], limits: v.Object["limits"]}
}
func TestAcceptPrepareCompleteInputAndIsolation(t *testing.T) {
	m := qualificationFixture(t, "subscription_direct")
	m.catalog.feed.Seeds[0].Title = "[Group] Show - 01 [1080p]"
	m.bgm = &anime.BGMBinding{WorkId: "123"}
	m.tmdb = &anime.TMDBBinding{WorkId: "456", MediaKind: "tv", Season: 1}
	var err error
	m.catalog.feed.ContentHash, err = catalog.CanonicalFeedSnapshotContentHash(m.catalog.feed.Seeds)
	if err != nil {
		t.Fatal(err)
	}
	c, err := constructQualification(m)
	if err != nil {
		t.Fatal(err)
	}
	intent := serviceIntentFixture(t, c)
	result, err := evaluateAdmission(context.Background(), c, intent, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.Validate("AcquisitionInputV1", result.input); err != nil {
		t.Fatal(err)
	}
	source := result.input.Object["source"].Object
	if count := source["torrent_file_count"].Object["value"].Int; count != int64(len(result.manifest.Files)) {
		t.Fatalf("torrent_file_count = %d, want the manifest's %d files", count, len(result.manifest.Files))
	}
	var size int64
	for _, file := range result.manifest.Files {
		size += file.SizeBytes
	}
	if got := source["torrent_size"].Object["value"].Int; got != size {
		t.Fatalf("torrent_size = %d, want the manifest total %d", got, size)
	}
	original := string(protocol.Canonical(result.input))
	req := requestIdentity{namespace: "manual_acquire_v1", key: "key", hash: c.requestHash, canonical: `{"command":"acquire"}`}
	p, err := prepareAdmission(c, result, intent, acceptAccess{scope: "owner"}, req, testWitnessLimits())
	if err != nil {
		t.Fatal(err)
	}
	if original != string(protocol.Canonical(result.input)) {
		t.Fatal("Prepare mutated evaluation")
	}
	if p.args.InputCanonicalUtf8.String != original || p.args.PreparationWitness.String == "" || p.args.Payload == nil {
		t.Fatal("incomplete prepared")
	}
	if _, ok := result.input.Object["config"].Object["vars"]; ok {
		t.Fatal("new input contains retired config vars")
	}
	if _, ok := result.input.Object["delivery"].Object["naming"].Object["vars_choice"]; ok {
		t.Fatal("new input contains retired vars choice")
	}
	tampered := result
	tampered.inputDigest = c.payloadDigest
	if _, err := prepareAdmission(c, tampered, intent, acceptAccess{scope: "owner"}, req, testWitnessLimits()); err == nil {
		t.Fatal("digest mismatch accepted")
	}
	if receipt := (CommittedAcceptance{}).Receipt(); len(receipt) != 0 {
		t.Fatal("zero commit exposed receipt")
	}
}

func TestAcceptEvaluateDoesNotBypassMetadataHold(t *testing.T) {
	m := qualificationFixture(t, "automatic_source")
	m.catalog.feed.Seeds[0].Title = "[Group] Show - 01 [1080p]"
	var err error
	m.catalog.feed.ContentHash, err = catalog.CanonicalFeedSnapshotContentHash(m.catalog.feed.Seeds)
	if err != nil {
		t.Fatal(err)
	}
	c, err := constructQualification(m)
	if err != nil {
		t.Fatal(err)
	}
	intent := serviceIntentFixture(t, c)
	intent.entryMode = "automatic_source"
	intent.media.Object["tmdb"], err = valueOf(map[string]any{"state": "unavailable", "value": nil, "reason_code": "MISSING_METADATA"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evaluateAdmission(context.Background(), c, intent, false); err == nil {
		t.Fatal("unknown metadata bypassed admission hold")
	}
}
