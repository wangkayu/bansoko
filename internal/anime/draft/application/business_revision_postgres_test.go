package application

import (
	"bansoko/internal/anime/animeconfig"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/testutil/animetest"
	"context"
	"encoding/json"
	"testing"
)

func TestBusinessRevisionActivationNeverCopiesChangedGlobal(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	service := draftNewTestService(t, db)
	draft, err := domainDraft(draftTestDraft(t))
	if err != nil {
		t.Fatal(err)
	}
	frozen := frozenBusinessFixture(t)
	draft.Business = frozen.Business
	draft.BusinessDigest = frozen.BusinessDigest
	seedBusinessPrograms(t, db, &draft)
	candidate, err := service.SaveAnimeConfigCandidate(ctx, CandidateDraftInput{Draft: draft})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := service.GetAnimeConfigCandidateRevision(ctx, candidate.Id, *candidate.CurrentRevision)
	if err != nil {
		t.Fatal(err)
	}
	if err := revision.VerifyIntegrity(); err != nil {
		t.Fatal(err)
	}
	if revision.Business == nil || revision.Business.RetentionVersion != 13 || revision.Business.UnclassifiedRoute != "route" {
		t.Fatalf("revision lost business: %+v", revision)
	}
	if _, err := db.ExecContext(ctx, `UPDATE global_config_sections SET version=version+1 WHERE section_name='retention'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO global_config_sections(section_name,schema_version,value,version,updated_at) VALUES('file_effect_policy','file-effect-policy-v1','{"unclassified_route":"skip"}'::jsonb,17,CURRENT_TIMESTAMP) ON CONFLICT (section_name) DO UPDATE SET value=EXCLUDED.value, version=global_config_sections.version+1, updated_at=CURRENT_TIMESTAMP`); err != nil {
		t.Fatal(err)
	}
	result, err := service.ActivateAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "activate-business-0001", CandidateSubscribeInput{Revision: *candidate.CurrentRevision, Start: true})
	if err != nil {
		t.Fatal(err)
	}
	var mode, route string
	var retentionVersion, fileVersion int64
	if err := db.QueryRowContext(ctx, `SELECT admission_mode,retention_source_default_version FROM anime_business_configs WHERE managed_anime_id=$1`, result.ManagedAnime.ID).Scan(&mode, &retentionVersion); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT unclassified_route,file_effect_source_default_version FROM anime_business_configs WHERE managed_anime_id=$1`, result.ManagedAnime.ID).Scan(&route, &fileVersion); err != nil {
		t.Fatal(err)
	}
	if mode != "single_copy" || retentionVersion != 13 || route != "route" || fileVersion != 17 {
		t.Fatalf("activation drift mode=%s retention=%d route=%s file=%d", mode, retentionVersion, route, fileVersion)
	}
	var raw []byte
	var digest string
	if err := db.QueryRowContext(ctx, `SELECT business_snapshot,business_digest FROM anime_business_configs WHERE managed_anime_id=$1`, result.ManagedAnime.ID).Scan(&raw, &digest); err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Programs struct {
			Vars struct {
				Digest string `json:"digest"`
			} `json:"vars"`
		} `json:"programs"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if digest != *draft.BusinessDigest || stored.Programs.Vars.Digest != draft.Business.Programs.Vars.Digest {
		t.Fatal("instance did not freeze exact revision programs")
	}
	for _, mutation := range []string{
		`UPDATE anime_business_configs SET business_snapshot=jsonb_set(business_snapshot,'{unclassified_route}','"skip"'), business_version=business_version+1 WHERE managed_anime_id=$1`,
		`UPDATE anime_business_configs SET business_digest='sha256:'||repeat('0',64), business_version=business_version+1 WHERE managed_anime_id=$1`,
	} {
		if _, err := db.ExecContext(ctx, mutation, result.ManagedAnime.ID); err == nil {
			t.Fatal("instance frozen guard accepted corrupt snapshot/version")
		}
	}
}

// Every per-anime section is one-time input.
// Activation stores each section as the complete value the draft carries, and a
// later Global change reaches none of them.
func TestBusinessRevisionActivationStoresOneTimeSections(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	service := draftNewTestService(t, db)
	draft, err := domainDraft(draftTestDraft(t))
	if err != nil {
		t.Fatal(err)
	}
	custom := json.RawMessage(`{"must_contain":"1080p","not_in":""}`)
	draft.Rules.Sections.ReleaseFilter = json.RawMessage(`{"mode":"replace","value":{"must_contain":"1080p","not_in":""}}`)
	frozen := frozenBusinessFixture(t)
	frozen.Business.Sections[animedomain.SectionReleaseRules] = custom
	business, err := frozen.Business.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := business.Digest()
	if err != nil {
		t.Fatal(err)
	}
	draft.Business, draft.BusinessDigest = &business, &digest
	seedBusinessPrograms(t, db, &draft)
	candidate, err := service.SaveAnimeConfigCandidate(ctx, CandidateDraftInput{Draft: draft})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ActivateAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "activate-one-time-0001", CandidateSubscribeInput{Revision: *candidate.CurrentRevision, Start: true})
	if err != nil {
		t.Fatal(err)
	}
	var nullRows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM anime_config_overrides WHERE managed_anime_id=$1 AND (value IS NULL OR schema_version IS NULL)`, result.ManagedAnime.ID).Scan(&nullRows); err != nil {
		t.Fatal(err)
	}
	if nullRows != 0 {
		t.Fatalf("activation stored %d sections without a value", nullRows)
	}

	// A later Global change reaches no stored section.
	for _, statement := range []string{
		`INSERT INTO global_config_sections(section_name,schema_version,value,version) VALUES('file_rules','file-rules-v1','{"must_contain":"mkv","not_in":""}'::jsonb,1) ON CONFLICT (section_name) DO UPDATE SET value=EXCLUDED.value, version=global_config_sections.version+1`,
		`INSERT INTO global_config_sections(section_name,schema_version,value,version) VALUES('release_rules','release-rules-v1','{"must_contain":"720p","not_in":""}'::jsonb,1) ON CONFLICT (section_name) DO UPDATE SET value=EXCLUDED.value, version=global_config_sections.version+1`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	resolved, err := animeconfig.LoadResolved(ctx, db, result.ManagedAnime.ID)
	if err != nil {
		t.Fatal(err)
	}
	var fileRules, releaseRules struct {
		MustContain string `json:"must_contain"`
	}
	if err := json.Unmarshal(resolved.Sections[animedomain.SectionFileRules], &fileRules); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(resolved.Sections[animedomain.SectionReleaseRules], &releaseRules); err != nil {
		t.Fatal(err)
	}
	if fileRules.MustContain != "" || releaseRules.MustContain != "1080p" {
		t.Fatalf("file rules=%q (want the stored empty value), release rules=%q (want the stored 1080p)", fileRules.MustContain, releaseRules.MustContain)
	}
}
