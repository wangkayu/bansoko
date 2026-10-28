package parserpolicy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"bansoko/internal/apperrors"
	"bansoko/internal/parser"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/testutil/disposabledb"
)

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db := disposabledb.ResetDisposableSchema(t)
	// The merged anime_business_configs row has a foreign key on managed_anime,
	// so the identities these owner tests provision must exist ("anime-missing"
	// deliberately does not).
	for _, animeID := range []string{"anime-a", "anime-b", "anime-cas", "anime-reeval", "anime-no-scheduler", "anime-reeval-error"} {
		if _, err := db.ExecContext(context.Background(), `INSERT INTO managed_anime(id, title) VALUES ($1, 'Parser')`, animeID); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestValidateUsesRuntimeRegistryAuthority(t *testing.T) {
	kind := parser.ParserKind("test-policy-runtime-parser")
	if err := parser.DefaultRegistry().Register(testParserAdapter{id: string(kind)}); err != nil {
		t.Fatalf("register test parser: %v", err)
	}
	if err := Validate(Policy{Kind: kind}); err != nil {
		t.Fatalf("registered parser policy rejected: %v", err)
	}
	if _, err := parser.DefaultRegistry().Resolve(string(kind)); err != nil {
		t.Fatalf("registered parser unavailable to runtime facade: %v", err)
	}
}

func TestValidateRejectsUnregisteredKind(t *testing.T) {
	err := Validate(Policy{Kind: "not-a-parser"})
	if err == nil {
		t.Fatal("unregistered parser kind must fail closed")
	}
}

type testParserAdapter struct{ id string }

func (a testParserAdapter) Descriptor() parser.ParserDescriptor {
	return parser.ParserDescriptor{ID: a.id, ImplementationVersion: "test-v1", OutputContractVersion: "episode-evidence-v1", ConfigVersion: "test-config-v1"}
}

func (testParserAdapter) ValidateConfig(json.RawMessage) error { return nil }

func (testParserAdapter) ParseDraft(context.Context, parser.EpisodeParseInput, json.RawMessage) (parser.EpisodeEvidenceDraft, error) {
	return parser.EpisodeEvidenceDraft{}, errors.New("not used")
}

func TestCopyDefaultThenGlobalChangeIsolatesInstances(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	service := NewService(db)

	// The shipped chain seeds the singleton template: the baseline writes it at
	// version 1 and forward migration 012's parser-kind rename bumps it, so the
	// row carries whatever version the chain last wrote. Read it rather than
	// assuming 1: CopyDefaultToInstanceTx copies it as the instance's provenance
	// and the CAS below starts from it.
	seeded, err := service.GetDefault(ctx)
	if err != nil || seeded == nil {
		t.Fatalf("get seeded default: %v, %v", seeded, err)
	}

	// The seeded row is present, so the first copy freezes the seeded template and
	// creates a version-1 instance.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CopyDefaultToInstanceTx(ctx, txscope.Wrap(tx), "anime-a"); err != nil {
		t.Fatalf("copy default: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	instance, err := service.GetInstance(ctx, "anime-a")
	if err != nil {
		t.Fatalf("get instance: %v", err)
	}
	if instance.Policy.Kind != parser.ParserReltitle {
		t.Fatalf("instance policy=%+v, want the seeded reltitle default", instance.Policy)
	}
	if instance.Version != 1 || instance.SourceDefaultVersion != seeded.Version {
		t.Fatalf("instance version=%d sourceDefault=%d, want 1/%d", instance.Version, instance.SourceDefaultVersion, seeded.Version)
	}

	// Global default changes (CAS seeded -> seeded+1).
	newKind := parser.ParserKind(parser.ParserRegex)
	saved, err := service.SaveDefault(ctx, seeded.Version, Policy{Kind: newKind})
	if err != nil {
		t.Fatalf("save default: %v", err)
	}
	if saved.Version != seeded.Version+1 {
		t.Fatalf("saved version=%d, want %d", saved.Version, seeded.Version+1)
	}

	// The existing instance must remain reltitle: no dynamic inheritance.
	instance, err = service.GetInstance(ctx, "anime-a")
	if err != nil {
		t.Fatalf("get instance: %v", err)
	}
	if instance.Policy.Kind != parser.ParserReltitle {
		t.Fatalf("instance kind=%s after global change, want reltitle (isolated)", instance.Policy.Kind)
	}

	// A second instance copies the new global default at creation time.
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CopyDefaultToInstanceTx(ctx, txscope.Wrap(tx), "anime-b"); err != nil {
		t.Fatalf("copy default b: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	instanceB, err := service.GetInstance(ctx, "anime-b")
	if err != nil {
		t.Fatalf("get instance b: %v", err)
	}
	if instanceB.Policy.Kind != newKind {
		t.Fatalf("instance b kind=%s, want %s (creation default)", instanceB.Policy.Kind, newKind)
	}
}

func TestSaveInstanceCASConflict(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	service := NewService(db)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CopyDefaultToInstanceTx(ctx, txscope.Wrap(tx), "anime-cas"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	_, err = service.SaveInstance(ctx, "anime-cas", 1, Policy{Kind: parser.ParserReltitle})
	if err != nil {
		t.Fatalf("save instance: %v", err)
	}
	_, err = service.SaveInstance(ctx, "anime-cas", 1, Policy{Kind: parser.ParserReltitle})
	var conflict *apperrors.VersionConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("stale CAS error=%v, want VersionConflict", err)
	}
}

func TestSaveInstanceMissingAnimeNotFound(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	service := NewService(db)
	_, err := service.SaveInstance(ctx, "anime-missing", 1, DefaultPolicy())
	var notFound *apperrors.NotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("error=%v, want NotFound", err)
	}
}

func seedParserInstance(t *testing.T, service *Service, animeID string) {
	t.Helper()
	tx, err := service.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CopyDefaultToInstanceTx(context.Background(), txscope.Wrap(tx), animeID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// reevaluationFake records the parser-policy reevaluation hook invocations.
type reevaluationFake struct {
	animeIDs []string
	err      error
}

func (f *reevaluationFake) ScheduleUnsubmittedReleaseReevaluationTx(_ context.Context, _ txscope.Tx, managedAnimeID string) error {
	f.animeIDs = append(f.animeIDs, managedAnimeID)
	return f.err
}

// A parser kind change invalidates every unsubmitted
// Release's parse evidence, so it must schedule reevaluation inside the save
// transaction; re-saving the same policy must not.
func TestSaveInstanceKindChangeSchedulesReleaseReevaluation(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	ctx := context.Background()
	service := NewService(db)
	seedParserInstance(t, service, "anime-reeval")

	newKind := parser.ParserKind("test-policy-reeval-kind")
	if err := parser.DefaultRegistry().Register(testParserAdapter{id: string(newKind)}); err != nil {
		t.Fatalf("register kind: %v", err)
	}
	hook := &reevaluationFake{}
	service.SetReleaseReevaluationScheduler(hook)

	// A same-policy save (kind unchanged) never schedules reevaluation and
	// never re-keys files for an offset: the policy carries no offset.
	if _, err := service.SaveInstance(ctx, "anime-reeval", 1, Policy{Kind: parser.ParserReltitle}); err != nil {
		t.Fatalf("unchanged-kind save: %v", err)
	}
	if len(hook.animeIDs) != 0 {
		t.Fatalf("unchanged-kind save reevaluation=%v, want none", hook.animeIDs)
	}

	// Kind change: exactly one reevaluation for this instance.
	saved, err := service.SaveInstance(ctx, "anime-reeval", 2, Policy{Kind: newKind})
	if err != nil {
		t.Fatalf("kind change save: %v", err)
	}
	if len(hook.animeIDs) != 1 || hook.animeIDs[0] != "anime-reeval" {
		t.Fatalf("kind change reevaluation=%v, want [anime-reeval]", hook.animeIDs)
	}
	if saved.Policy.Kind != newKind || saved.Version != 3 {
		t.Fatalf("saved=%+v, want kind=%s version=3", saved, newKind)
	}

	// Re-saving the same kind is not a change: no further
	// reevaluation.
	if _, err := service.SaveInstance(ctx, "anime-reeval", 3, Policy{Kind: newKind}); err != nil {
		t.Fatalf("same-policy save: %v", err)
	}
	if len(hook.animeIDs) != 1 {
		t.Fatalf("same-policy save scheduled another reevaluation: %v", hook.animeIDs)
	}
}

// A kind change whose reevaluation cannot be scheduled must not commit: the
// policy would otherwise silently change with unsubmitted Releases still
// holding old parse evidence.
func TestSaveInstanceKindChangeFailsClosedWithoutScheduler(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	ctx := context.Background()
	service := NewService(db)
	seedParserInstance(t, service, "anime-no-scheduler")

	newKind := parser.ParserKind("test-policy-no-scheduler-kind")
	if err := parser.DefaultRegistry().Register(testParserAdapter{id: string(newKind)}); err != nil {
		t.Fatalf("register kind: %v", err)
	}

	if _, err := service.SaveInstance(ctx, "anime-no-scheduler", 1, Policy{Kind: newKind}); err == nil {
		t.Fatal("kind change without a reevaluation scheduler must fail closed")
	}
	instance, err := service.GetInstance(ctx, "anime-no-scheduler")
	if err != nil {
		t.Fatal(err)
	}
	if instance.Policy.Kind != parser.ParserReltitle || instance.Version != 1 {
		t.Fatalf("failed kind change was not rolled back: %+v", instance)
	}
}

func TestSaveInstanceKindChangeSchedulerErrorRollsBack(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	ctx := context.Background()
	service := NewService(db)
	seedParserInstance(t, service, "anime-reeval-error")

	newKind := parser.ParserKind("test-policy-reeval-error-kind")
	if err := parser.DefaultRegistry().Register(testParserAdapter{id: string(newKind)}); err != nil {
		t.Fatalf("register kind: %v", err)
	}
	service.SetReleaseReevaluationScheduler(&reevaluationFake{err: errors.New("scheduler failed")})

	if _, err := service.SaveInstance(ctx, "anime-reeval-error", 1, Policy{Kind: newKind}); err == nil {
		t.Fatal("scheduler failure must fail the save")
	}
	instance, err := service.GetInstance(ctx, "anime-reeval-error")
	if err != nil {
		t.Fatal(err)
	}
	if instance.Policy.Kind != parser.ParserReltitle || instance.Version != 1 {
		t.Fatalf("scheduler failure lost the rollback: %+v", instance)
	}
}

// Switching between the real catalog kinds saves, schedules exactly one
// reevaluation for the instance (unsubmitted releases only, enforced by the
// scheduler owner), and refuses a kind outside the catalog.
func TestSaveInstanceSwitchesToRegex(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	ctx := context.Background()
	service := NewService(db)
	seedParserInstance(t, service, "anime-reeval")
	hook := &reevaluationFake{}
	service.SetReleaseReevaluationScheduler(hook)

	saved, err := service.SaveInstance(ctx, "anime-reeval", 1, Policy{Kind: parser.ParserKind(parser.ParserRegex)})
	if err != nil {
		t.Fatalf("switch to regex: %v", err)
	}
	if saved.Policy.Kind != parser.ParserRegex || len(hook.animeIDs) != 1 {
		t.Fatalf("saved=%+v reevaluations=%v, want regex with one reevaluation", saved, hook.animeIDs)
	}
	if _, err := service.SaveInstance(ctx, "anime-reeval", saved.Version, Policy{Kind: "reltitle-v2"}); err == nil {
		t.Fatal("a kind outside the catalog must be refused")
	}
}
