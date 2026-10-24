package application

import (
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/attrrules"
	"bansoko/internal/namingvars"
	"bansoko/internal/testutil/animetest"
	"context"
	"database/sql"
	"encoding/json"
	"testing"
)

func seedBusinessPrograms(t *testing.T, db *sql.DB, draft *animedomain.AnimeDraft) {
	t.Helper()
	ctx := context.Background()
	source := `vars.release = {title: input.release_title};`
	fp, err := namingvars.SourceFingerprint(source)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO naming_vars_versions(fingerprint,content,active) VALUES($1,$2,FALSE) ON CONFLICT(fingerprint) DO NOTHING`, fp, raw); err != nil {
		t.Fatal(err)
	}
	rules := businessProgramRulesFixture()
	attrFP, err := attrrules.Fingerprint(rules)
	if err != nil {
		t.Fatal(err)
	}
	attrRaw, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO attr_rule_versions(fingerprint,content,active) VALUES($1,$2,FALSE) ON CONFLICT(fingerprint) DO NOTHING`, attrFP, attrRaw); err != nil {
		t.Fatal(err)
	}
	draft.Business.Programs = animedomain.ProgramReferences{Vars: namingvars.ProgramRef{Digest: fp, Version: namingvars.ProfileVersion(fp)}, AttrRules: namingvars.ProgramRef{Digest: attrFP, Version: attrrules.ProfileVersion(attrFP)}}
	digest, err := draft.Business.Digest()
	if err != nil {
		t.Fatal(err)
	}
	draft.BusinessDigest = &digest
}
func TestBusinessSaveRequiresExactPersistedPrograms(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	ctx := context.Background()
	draft, err := domainDraft(draftTestDraft(t))
	if err != nil {
		t.Fatal(err)
	}
	b := frozenBusinessFixture(t)
	draft.Business = b.Business
	draft.BusinessDigest = b.BusinessDigest
	if _, err := service.SaveAnimeConfigCandidate(ctx, CandidateDraftInput{Draft: draft}); err == nil {
		t.Fatal("missing program was accepted")
	}
	if got := animetest.CountRows(t, db, "anime_config_candidates"); got != 0 {
		t.Fatalf("failed save wrote %d candidates", got)
	}
	seedBusinessPrograms(t, db, &draft)
	candidate, err := service.SaveAnimeConfigCandidate(ctx, CandidateDraftInput{Draft: draft})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := service.GetAnimeConfigCandidateRevision(ctx, candidate.Id, *candidate.CurrentRevision)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Business.Programs != draft.Business.Programs {
		t.Fatal("revision substituted active program for exact persisted ref")
	}
}
