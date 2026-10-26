package filemaintenance

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"bansoko/internal/library/domain"
	"bansoko/internal/postprocessing/naming"
)

// A placement re-attached by a REAL confirm carries the typed metadata
// envelope; calibration's identity read (placementObservationConflictTx) must
// see the inode inside it. A same-size different-inode file at the recovered
// target is fingerprint_conflict; the recovered file itself is not.
func TestRecoveredPlacementCalibratesSameSizeOtherInodeAsConflict(t *testing.T) {
	scene := newRecoveryApplicationScene(t)
	service := scene.service(&staticObserver{files: scene.files})
	if _, err := service.RecoverAnimeFileApplication(context.Background(), scene.animeID, scene.command()); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	ctx := context.Background()
	tx, err := scene.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for observed, want := range map[string]bool{
		"meta:size=4096;dev=2;ino=11": false, // the recovered file
		"meta:size=4096;dev=9;ino=11": false, // a device-only remount
		"meta:size=4096;dev=2;ino=99": true,  // same size, replaced inode
		"meta:size=4097;dev=2;ino=11": true,  // size changed
	} {
		conflict, err := placementObservationConflictTx(ctx, tx, scene.candidate.PlacementID, observed)
		if err != nil {
			t.Fatalf("%s: %v", observed, err)
		}
		if conflict != want {
			t.Fatalf("%s conflict = %v, want %v", observed, conflict, want)
		}
	}
}

// ownerRowLocked reports, from a separate connection, whether the confirm's
// transaction currently holds its managed_anime FOR SHARE lock (the first lock
// it takes and the last it releases). FOR UPDATE NOWAIT conflicts with it and
// answers lock_not_available (SQLSTATE 55P03) instead of waiting.
func ownerRowLocked(t *testing.T, db *sql.DB, animeID string) bool {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Errorf("begin lock probe: %v", err)
		return false
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRow(`SELECT id FROM managed_anime WHERE id = $1 FOR UPDATE NOWAIT`, animeID).Scan(&id)
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "55P03" {
		return true
	}
	t.Errorf("lock probe: %v", err)
	return false
}

// lockProbe records every render and stat the confirm performs together with
// whether the confirm's transaction held its owner lock at that moment.
type lockProbe struct {
	t       *testing.T
	db      *sql.DB
	animeID string

	mu            sync.Mutex
	renders       int
	lockedRenders int
	stats         []string
	lockedStats   []string
}

func (p *lockProbe) record(path string, render bool) {
	locked := ownerRowLocked(p.t, p.db, p.animeID)
	p.mu.Lock()
	defer p.mu.Unlock()
	if render {
		p.renders++
		if locked {
			p.lockedRenders++
		}
		return
	}
	p.stats = append(p.stats, path)
	if locked {
		p.lockedStats = append(p.lockedStats, path)
	}
}

type probedRulesSource struct {
	NamingRulesSource
	probe *lockProbe
}

func (r probedRulesSource) EffectiveNamingRules(ctx context.Context, animeID string) (naming.NamingRules, error) {
	r.probe.record("", true)
	return r.NamingRulesSource.EffectiveNamingRules(ctx, animeID)
}

// probedObserver answers from a table like staticObserver; after the first
// stat of `swapPath` it answers `swapped` for that path (a file replaced
// between the preflight and the transaction).
type probedObserver struct {
	probe    *lockProbe
	files    map[string]domain.FileObservation
	swapPath string
	swapped  domain.FileObservation

	mu   sync.Mutex
	seen map[string]int
}

func (o *probedObserver) ObserveMetadataFile(_ context.Context, path string) (domain.FileObservation, error) {
	o.probe.record(path, false)
	o.mu.Lock()
	if o.seen == nil {
		o.seen = map[string]int{}
	}
	o.seen[path]++
	count := o.seen[path]
	o.mu.Unlock()
	if path == o.swapPath && count > 1 {
		return o.swapped, nil
	}
	if observation, ok := o.files[path]; ok {
		return observation, nil
	}
	return domain.FileObservation{}, errors.New("not found")
}

func (s *recoveryApplicationScene) probedService(probe *lockProbe, observer RecoveryFileObserver) *Service {
	renderer := NewNamingTargetRenderer(s.db, probedRulesSource{
		NamingRulesSource: fakeNamingRulesSource{rules: s.rules}, probe: probe,
	})
	service := NewService(nil, nil, nil, NewPlacementService(s.db, nil, nil), nil)
	service.SetPathDiff(NewPathDiffService(s.db, renderer, nil))
	service.SetRecoveryObserver(observer)
	return service
}

// No render and no companion stat ever runs while the confirm holds its
// owner/subscription/placement locks; the only file I/O under the locks is ONE
// stat of the target, after the unit advisory locks.
func TestRecoverAnimeFileApplicationDoesNoRenderOrUnitStatUnderLocks(t *testing.T) {
	scene := newRecoveryApplicationScene(t)
	probe := &lockProbe{t: t, db: scene.db, animeID: scene.animeID}
	service := scene.probedService(probe, &probedObserver{probe: probe, files: scene.files})

	if _, err := service.RecoverAnimeFileApplication(context.Background(), scene.animeID, scene.command()); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if probe.renders == 0 {
		t.Fatal("the confirm never re-rendered the unit")
	}
	if probe.lockedRenders != 0 {
		t.Fatalf("%d of %d renders ran while the confirm held its locks", probe.lockedRenders, probe.renders)
	}
	if len(probe.lockedStats) != 1 || probe.lockedStats[0] != scene.candidate.TargetPath {
		t.Fatalf("stats under the confirm's locks = %v, want exactly one stat of the target %q", probe.lockedStats, scene.candidate.TargetPath)
	}
	// Preflight: target + every companion; locked: the target once more.
	if want := 2 + len(scene.candidate.Companions); len(probe.stats) != want {
		t.Fatalf("total stats = %v, want %d", probe.stats, want)
	}
}

// The target replaced after the preflight (same size, different inode)
// is caught by the single re-stat under the unit lock: target_changed, and no
// ownership is written.
func TestRecoverAnimeFileApplicationRefusesTargetChangedAfterPreflight(t *testing.T) {
	scene := newRecoveryApplicationScene(t)
	probe := &lockProbe{t: t, db: scene.db, animeID: scene.animeID}
	observer := &probedObserver{
		probe: probe, files: scene.files, swapPath: scene.candidate.TargetPath,
		swapped: domain.FileObservation{Fingerprint: "meta:size=4096;dev=2;ino=99", SizeBytes: 4096},
	}
	scene.assertRefusal(t, scene.probedService(probe, observer), RecoveryReasonTargetChanged)
	if len(probe.lockedStats) != 1 {
		t.Fatalf("stats under the confirm's locks = %v, want the single target re-stat", probe.lockedStats)
	}
}
