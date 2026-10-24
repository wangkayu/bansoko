//go:build soakscale

package soak

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// dataset describes the loaded database: which owners the read tests probe.
type dataset struct {
	Profile      Profile
	Loaded       bool // false when an existing dataset was reused
	LoadElapsed  time.Duration
	TypicalOwner string // managed_anime id of an owner with ~TypicalReleases releases
	TypicalSub   string
	HeavyOwners  []string
	ShapeDiffs   []string
	Counts       map[string]int64
	DBSize       int64
}

var (
	dsOnce sync.Once
	ds     *dataset
	dsErr  error
)

// loadDataset loads (or finds) the profile dataset once per process; the three
// measurement tests and the dataset test share it.
func loadDataset(t *testing.T) *dataset {
	t.Helper()
	dsOnce.Do(func() { ds, dsErr = doLoadDataset(t) })
	if dsErr != nil {
		t.Fatalf("dataset: %v", dsErr)
	}
	if ds == nil {
		t.Fatalf("dataset: the load did not complete (an earlier test failed while loading it)")
	}
	return ds
}

func doLoadDataset(t *testing.T) (*dataset, error) {
	e := getEnv(t)
	owner, rt := dbs(t)
	ctx := context.Background()
	start := time.Now()
	if err := applyMigrations(ctx, e, owner); err != nil {
		return nil, fmt.Errorf("migrations: %w", err)
	}
	if _, err := owner.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS soak_ext.soak_meta (key text PRIMARY KEY, value text NOT NULL)`); err != nil {
		return nil, err
	}
	d := &dataset{Profile: e.Profile}
	var loaded string
	err := owner.QueryRowContext(ctx, `SELECT value FROM soak_ext.soak_meta WHERE key='dataset_loaded'`).Scan(&loaded)
	switch {
	case err == nil:
		t.Logf("dataset already loaded (%s): reusing", loaded)
		if err := installRegistries(rt); err != nil {
			return nil, err
		}
	case err == sql.ErrNoRows:
		if err := installRegistries(rt); err != nil {
			return nil, err
		}
		var existing int
		if err := owner.QueryRowContext(ctx, `SELECT count(*) FROM managed_anime`).Scan(&existing); err != nil {
			return nil, err
		}
		svc, err := newOwnerServices(rt)
		if err != nil {
			return nil, err
		}
		if existing == 0 {
			if _, err := svc.subscribeTemplate(ctx, "soak-template-key", "template", "https://feeds.soak.invalid", 1); err != nil {
				return nil, fmt.Errorf("template subscribe: %w", err)
			}
		}
		if err := os.MkdirAll(libraryRootPath(), 0o755); err != nil {
			return nil, err
		}
		gen := newGenerator(t, e, owner)
		gen.timeBase()
		if _, err := gen.conn.ExecContext(ctx, `SET session_replication_role = replica`); err != nil {
			return nil, fmt.Errorf("replica role (the harness needs a superuser): %w", err)
		}
		gen.generateOwners(ctx)
		gen.generateReleases(ctx)
		gen.generateGraph(ctx)
		gen.generateMisc(ctx)
		if _, err := gen.conn.ExecContext(ctx, `SET session_replication_role = origin`); err != nil {
			return nil, err
		}
		t.Logf("generator timings:\n%s", gen.summary())
		writeEvidenceFile(t, "generator-timings.txt", gen.summary())
		realSample(t, owner, e.Profile)
		for _, tbl := range []string{"owner", "src", "rel", "acq", "dl", "src_rn", "acq_rn", "dl_rn", "dl_rn_recent", "dl_rn_old"} {
			if _, err := owner.ExecContext(ctx, `DROP TABLE IF EXISTS soak_ext.`+tbl); err != nil {
				return nil, err
			}
		}
		vacStart := time.Now()
		if err := vacuumAnalyze(ctx, owner); err != nil {
			return nil, err
		}
		t.Logf("VACUUM ANALYZE of all tables: %v", time.Since(vacStart).Round(time.Second))
		d.Loaded = true
		d.LoadElapsed = time.Since(start)
		runConsistencyChecks(t, owner)
		d.ShapeDiffs = compareShapes(t, owner)
		if _, err := owner.ExecContext(ctx, `INSERT INTO soak_ext.soak_meta VALUES ('dataset_loaded', $1) ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
			fmt.Sprintf("profile=%s at %s", e.Profile.Name, time.Now().UTC().Format(time.RFC3339))); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	var qerr error
	d.Counts, qerr = tableCounts(ctx, owner)
	if qerr != nil {
		return nil, qerr
	}
	// probe owners: heavy = the owners with the most releases, typical = closest to the profile average.
	rows, err := owner.QueryContext(ctx, `
SELECT m.id FROM managed_anime m JOIN subscriptions s ON s.managed_anime_id = m.id JOIN rss_sources r ON r.subscription_id = s.id
JOIN releases x ON x.rss_source_id = r.id WHERE s.origin <> 'oneshot' AND x.id LIKE 'rl%' GROUP BY m.id ORDER BY count(*) DESC, m.id LIMIT $1`, e.Profile.HeavyOwners)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		d.HeavyOwners = append(d.HeavyOwners, id)
	}
	rows.Close()
	if err := owner.QueryRowContext(ctx, `
SELECT m.id, s.id FROM managed_anime m JOIN subscriptions s ON s.managed_anime_id = m.id JOIN rss_sources r ON r.subscription_id = s.id
JOIN releases x ON x.rss_source_id = r.id WHERE s.origin <> 'oneshot' AND s.state = 'active' AND x.id LIKE 'rl%'
GROUP BY m.id, s.id HAVING count(*) BETWEEN $1 AND $2 ORDER BY abs(count(*) - $3), m.id LIMIT 1`,
		e.Profile.TypicalReleases-5, e.Profile.TypicalReleases+5, e.Profile.TypicalReleases).Scan(&d.TypicalOwner, &d.TypicalSub); err != nil {
		return nil, fmt.Errorf("typical owner: %w", err)
	}
	d.DBSize = tablesExport(t, owner, "dataset")
	return d, nil
}

// vacuumAnalyze brings visibility maps, hint bits and statistics to the state
// autovacuum would have reached on a real database that loaded this slowly.
func vacuumAnalyze(ctx context.Context, owner *sql.DB) error {
	rows, err := owner.QueryContext(ctx, `SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind='r' ORDER BY pg_total_relation_size(c.oid) DESC`)
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return err
		}
		names = append(names, n)
	}
	rows.Close()
	conn, err := owner.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	for _, n := range names {
		if _, err := conn.ExecContext(ctx, `VACUUM (ANALYZE) public.`+quoteIdent(n)); err != nil {
			return fmt.Errorf("vacuum %s: %w", n, err)
		}
	}
	return nil
}

// TestScaleDataset loads the profile, runs the consistency self-check and the
// real-versus-generated shape comparison, and reports row counts and timings.
func TestScaleDataset(t *testing.T) {
	e := getEnv(t)
	if !e.Phases["dataset"] && !e.Phases["read"] && !e.Phases["worker"] && !e.Phases["retention"] {
		t.Skip("no phase selected")
	}
	d := loadDataset(t)
	var b strings.Builder
	fmt.Fprintf(&b, "profile %s: load %v (%s)\n", d.Profile.Name, d.LoadElapsed.Round(time.Second), map[bool]string{true: "generated", false: "reused"}[d.Loaded])
	fmt.Fprintf(&b, "typical owner %s, heavy owners %v\ndatabase size %d bytes\n\n%s", d.TypicalOwner, d.HeavyOwners, d.DBSize, formatCounts(d.Counts))
	writeEvidenceFile(t, "dataset-counts.txt", b.String())
	t.Log("\n" + b.String())
	if len(d.ShapeDiffs) > 0 {
		for _, diff := range d.ShapeDiffs {
			t.Errorf("shape comparison (real accept path vs generated): %s", diff)
		}
	} else if d.Loaded {
		t.Log("shape comparison: no differences")
	}
}
