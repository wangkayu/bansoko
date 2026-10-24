package application

import (
	target "bansoko/internal/acquisition/target"
	persistence "bansoko/internal/acquisition/target/adapter/persistence"
	draft "bansoko/internal/anime/draft/application"
	"bansoko/internal/namingvars"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/frozenpolicy"
	"context"
	"encoding/json"
	"testing"
)

func TestOneShotAdmissionFreezesHistoricalVarsRealInventory(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	payload := parseOneShotTestTorrent(t)
	service := oneShotNewTestService(t, db, &oneShotFakeTorrentFetcher{payload: payload}, nil)
	service.SetTargetAcquisition(target.FilesystemProbe{}, target.NewStore(persistence.NewRepository(db)))
	animetest.SeedQbConnection(t, db)
	business, err := frozenpolicy.SeedBusiness(ctx, db, `vars.actual = {title: input.release_title, path: input.files[0].path, size: input.files[0].size};`)
	if err != nil {
		t.Fatal(err)
	}
	inputDraft := agentDraftWithLabel(t)
	inputDraft.Business = &business
	digest, err := business.Digest()
	if err != nil {
		t.Fatal(err)
	}
	inputDraft.BusinessDigest = &digest
	candidate, err := service.Draft.SaveAnimeConfigCandidate(ctx, draft.CandidateDraftInput{Draft: inputDraft})
	if err != nil {
		t.Fatal(err)
	}
	seed := oneShotAgentSeedForPayload(t, payload)
	recordOneShotSeedGroup(t, db, seed, oneShotSeedGroupName)
	if err := namingvars.SetActive(namingvars.Vars{"actual": {"title": "WRONG"}}, "new-active"); err != nil {
		t.Fatal(err)
	}
	_, err = service.AcquireSeedOnce(ctx, AcquireSeedInput{CandidateID: candidate.Id, Revision: *candidate.CurrentRevision, Seed: seed, Caller: AcquireSeedCaller{Kind: AcquireSeedCallerAgent, Agent: &AgentSeedAuthorization{GrantedBy: "pg-test", AuthorizationRef: "vars"}}}, "one-shot-vars-0001")
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := db.QueryRowContext(ctx, `SELECT frozen_policy FROM download_tasks`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var policy struct {
		Vars namingvars.FrozenEvaluation `json:"vars"`
	}
	if err := json.Unmarshal(raw, &policy); err != nil {
		t.Fatal(err)
	}
	if policy.Vars.Program != business.Programs.Vars || policy.Vars.Output["actual"]["title"] != "[Lilith-Raws] Example Anime - 01 [1080p]" || len(policy.Vars.Input.Files) != 1 {
		t.Fatalf("frozen admission=%s", raw)
	}
	// Exact manifest facts are compared independently against parsed torrent bytes.
	file := payload.Metadata.Files[0]
	if policy.Vars.Input.Files[0].Path != file.Path || policy.Vars.Input.Files[0].Size != file.SizeBytes {
		t.Fatalf("manifest drift: %+v vs %+v", policy.Vars.Input.Files[0], file)
	}
}
