//go:build soak

// Soak-only (scripts/ci/test-soak.sh): 2 orders x 50 rounds of the
// retirement-versus-acceptance race. Not part of the canonical gate.

package migrations

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	animepersistence "bansoko/internal/anime/adapter/persistence"
	ownership "bansoko/internal/anime/ownership/application"
	"bansoko/internal/workitems"
)

func TestAnimeRetirementApplicationVersusAcceptanceRace(t *testing.T) {
	for _, first := range []string{"accept", "retire"} {
		t.Run(first, func(t *testing.T) {
			for round := 0; round < 50; round++ {
				t.Run(fmt.Sprintf("round-%02d", round), func(t *testing.T) {
					owner, db, _ := progressProbe(t, fmt.Sprintf("retire-accept-%s-%d", first, round), nil, nil)
					ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					defer cancel()
					seedAcceptableRelease(t, owner, "race")
					p := validQueued(t, "acq-race", "key-race", "race")
					store := &workitems.Store{DB: db, RetentionDays: 7}
					svc, err := ownership.NewService(db, ownership.Dependencies{WorkItems: store, OneShotMarkers: animepersistence.NewRepository(db)})
					if err != nil {
						t.Fatal(err)
					}
					retire := func() error {
						return svc.DeleteAnimeSubscription(ctx, "anime-race", ownership.SubscriptionDeleteInput{ExpectedVersion: 1})
					}
					if first == "retire" {
						if err := retire(); err != nil {
							t.Fatal(err)
						}
						if _, err := callQueuedAccept(t, db, p); err == nil || !strings.Contains(err.Error(), "ACQ_QUALIFICATION_MANAGEMENT_CHANGED") {
							t.Fatalf("retired acceptance=%v", err)
						}
					} else {
						tx, err := db.BeginTx(ctx, nil)
						if err != nil {
							t.Fatal(err)
						}
						defer tx.Rollback()
						var pid int
						if err := tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
							t.Fatal(err)
						}
						if _, err := callQueuedAccept(t, tx, p); err != nil {
							t.Fatal(err)
						}
						done := make(chan error, 1)
						go func() { done <- retire() }()
						if err := waitRetirementApplicationBlocked(ctx, owner, pid); err != nil {
							tx.Rollback()
							<-done
							t.Fatal(err)
						}
						if err := tx.Commit(); err != nil {
							t.Fatal(err)
						}
						if err := <-done; err != nil {
							t.Fatal(err)
						}
					}
					var roots, live int
					var retired bool
					if err := owner.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM acquisitions WHERE id='acq-race'),(SELECT count(*) FROM work_items WHERE managed_anime_id='anime-race' AND state IN ('available','leased') AND external_effect_admitted_at IS NULL),lifecycle_state='retired' FROM managed_anime WHERE id='anime-race'`).Scan(&roots, &live, &retired); err != nil {
						t.Fatal(err)
					}
					want := 0
					if first == "accept" {
						want = 1
					}
					if roots != want || live != 0 || !retired {
						t.Fatalf("roots=%d want=%d live=%d retired=%t", roots, want, live, retired)
					}
				})
				if t.Failed() {
					return
				}
			}
		})
		if t.Failed() {
			return
		}
	}
}
