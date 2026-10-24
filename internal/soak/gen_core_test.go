//go:build soakscale

package soak

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// generator loads one profile into the database as the owner role. The load runs
// on a single connection with session_replication_role = replica (triggers and
// foreign-key triggers off; CHECK constraints stay on), so the accepted-download
// graph can be written directly. Everything is deterministic: ids and values
// derive from md5(seed, n), the time base is stored once.
type generator struct {
	t        *testing.T
	e        *soakEnv
	p        Profile
	conn     *sql.Conn
	base     time.Time
	baseSQL  string
	library  string // absolute library root used in placement paths
	template templateOwner
	timings  []stepTiming
}

type stepTiming struct {
	Step    string
	Rows    int64
	Elapsed time.Duration
}

// templateOwner identifies the owner created through the real subscribe path;
// the other owners are clones of its rows.
type templateOwner struct {
	AnimeID, SubscriptionID, SourceID string
}

func libraryRootPath() string { return filepath.Join(os.TempDir(), "soak-library") }

func (g *generator) exec(step, query string) int64 {
	g.t.Helper()
	start := time.Now()
	res, err := g.conn.ExecContext(context.Background(), query)
	if err != nil {
		g.t.Fatalf("generator step %s: %v", step, err)
	}
	n, _ := res.RowsAffected()
	el := time.Since(start)
	g.timings = append(g.timings, stepTiming{Step: step, Rows: n, Elapsed: el})
	g.t.Logf("gen %-44s rows=%-8d %v", step, n, el.Round(time.Millisecond))
	return n
}

// execf is exec over a Sprintf'd statement (numbers are inlined: every value
// comes from the profile, never from outside input).
func (g *generator) execf(step, format string, args ...any) int64 {
	g.t.Helper()
	return g.exec(step, fmt.Sprintf(format, args...))
}

func (g *generator) scalarInt(query string) int64 {
	g.t.Helper()
	var v sql.NullInt64
	if err := g.conn.QueryRowContext(context.Background(), query).Scan(&v); err != nil {
		g.t.Fatalf("query %q: %v", query, err)
	}
	return v.Int64
}

// helperFunctions installs the deterministic pseudo-random and digest helpers in
// the extension schema (outside public, so nothing in the product schema moves).
const helperFunctions = `
CREATE OR REPLACE FUNCTION soak_ext.rnd(k text, n bigint) RETURNS double precision
  LANGUAGE sql IMMUTABLE PARALLEL SAFE AS
  $$ SELECT (('x' || substr(md5(k || ':' || n::text), 1, 8))::bit(32)::bigint)::double precision / 4294967296.0 $$;
CREATE OR REPLACE FUNCTION soak_ext.hexn(k text, n bigint, len int) RETURNS text
  LANGUAGE sql IMMUTABLE PARALLEL SAFE AS
  $$ SELECT left(md5(k || ':' || n::text || ':a') || md5(k || ':' || n::text || ':b') || md5(k || ':' || n::text || ':c') || md5(k || ':' || n::text || ':d'), len) $$;
CREATE OR REPLACE FUNCTION soak_ext.dg(prefix text, body text) RETURNS text
  LANGUAGE sql IMMUTABLE PARALLEL SAFE AS
  $$ SELECT 'sha256:' || encode(sha256(convert_to(prefix, 'UTF8') || '\x00'::bytea || convert_to(body, 'UTF8')), 'hex') $$;
CREATE OR REPLACE FUNCTION soak_ext.pick(k text, n bigint, m bigint) RETURNS bigint
  LANGUAGE sql IMMUTABLE PARALLEL SAFE AS
  $$ SELECT 1 + floor(soak_ext.rnd(k, n) * m)::bigint $$;
CREATE OR REPLACE FUNCTION soak_ext.ep(k bigint) RETURNS text
  LANGUAGE sql IMMUTABLE PARALLEL SAFE AS
  $$ SELECT CASE WHEN k < 100 THEN lpad(k::text, 2, '0') ELSE k::text END $$;
CREATE OR REPLACE FUNCTION soak_ext.pad(n bigint, len int) RETURNS text
  LANGUAGE sql IMMUTABLE PARALLEL SAFE AS
  $$ SELECT left(repeat(md5(n::text) || ' ', (len / 33) + 1), len) $$;
`

// newGenerator prepares the load connection. It does not generate yet.
func newGenerator(t *testing.T, e *soakEnv, owner *sql.DB) *generator {
	t.Helper()
	ctx := context.Background()
	conn, err := owner.Conn(ctx)
	if err != nil {
		t.Fatalf("generator connection: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	g := &generator{t: t, e: e, p: e.Profile, conn: conn, library: libraryRootPath()}
	if _, err := conn.ExecContext(ctx, helperFunctions); err != nil {
		t.Fatalf("helper functions: %v", err)
	}
	return g
}

// timeBase returns the dataset time base: stored on first use so a reload (or a
// second process against the same database) sees the same instant.
func (g *generator) timeBase() {
	g.t.Helper()
	ctx := context.Background()
	if _, err := g.conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS soak_ext.soak_meta (key text PRIMARY KEY, value text NOT NULL)`); err != nil {
		g.t.Fatal(err)
	}
	var v string
	err := g.conn.QueryRowContext(ctx, `SELECT value FROM soak_ext.soak_meta WHERE key='base_time'`).Scan(&v)
	if err == sql.ErrNoRows {
		v = time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
		if _, err := g.conn.ExecContext(ctx, `INSERT INTO soak_ext.soak_meta VALUES ('base_time', $1)`, v); err != nil {
			g.t.Fatal(err)
		}
	} else if err != nil {
		g.t.Fatal(err)
	}
	b, err := time.Parse(time.RFC3339, v)
	if err != nil {
		g.t.Fatal(err)
	}
	g.base = b
	g.baseSQL = fmt.Sprintf("'%s'::timestamptz", v)
}

// summary renders the timings table the generator prints.
func (g *generator) summary() string {
	var b strings.Builder
	var total time.Duration
	for _, s := range g.timings {
		total += s.Elapsed
		fmt.Fprintf(&b, "%-48s %10d rows %12v\n", s.Step, s.Rows, s.Elapsed.Round(time.Millisecond))
	}
	fmt.Fprintf(&b, "%-48s %10s      %12v\n", "TOTAL", "", total.Round(time.Millisecond))
	return b.String()
}

// tableCounts returns exact row counts of every public table with rows.
func tableCounts(ctx context.Context, db *sql.DB) (map[string]int64, error) {
	rows, err := db.QueryContext(ctx, `SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind='r' ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		names = append(names, n)
	}
	rows.Close()
	out := map[string]int64{}
	for _, n := range names {
		var c int64
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM public.`+n).Scan(&c); err != nil {
			return nil, err
		}
		if c > 0 {
			out[n] = c
		}
	}
	return out, nil
}

func formatCounts(c map[string]int64) string {
	names := make([]string, 0, len(c))
	for n := range c {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "%-40s %10d\n", n, c[n])
	}
	return b.String()
}
