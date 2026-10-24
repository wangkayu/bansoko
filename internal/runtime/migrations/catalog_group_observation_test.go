package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	protocol "bansoko/internal/acquisition/terminal/domain"
	catalogapp "bansoko/internal/catalog/application"
	catalogdomain "bansoko/internal/catalog/domain"
	provider "bansoko/internal/provider/domain"
	"bansoko/internal/runtime/database"
)

func groupResult() provider.GroupReadResult {
	now := time.Now().UTC().Truncate(time.Microsecond)
	no, yes := false, true
	groups := []provider.GroupSnapshotEntry{}
	for i, ref := range []string{"20", "10"} {
		active := &yes
		if i == 0 {
			active = &no
		}
		key := provider.GroupRef{Provider: provider.ProviderMikan, ExternalID: ref}
		groups = append(groups, provider.GroupSnapshotEntry{Ref: key, DisplayName: "字幕 <組> & " + ref, ObservedAt: now, Active: active, FeedLocator: provider.FeedLocator{Provider: key.Provider, CatalogEntryRef: "123", Group: key}})
	}
	return provider.GroupReadResult{Provider: provider.ProviderMikan, CatalogEntryRef: "123", ObservedAt: now, SchemaVersion: "mikan-detail-groups-v1", Complete: true, CompletenessEvidence: "mikan-detail-group-blocks-v1", Groups: groups}
}

// groupEntryProjection renders the locator projection the snapshot's entries
// column stores for one read result, the same shape the publication writes.
func groupEntryProjection(r provider.GroupReadResult) []map[string]any {
	entries := make([]map[string]any, 0, len(r.Groups))
	for _, g := range r.Groups {
		locator, err := json.Marshal(g.FeedLocator)
		if err != nil {
			continue
		}
		entries = append(entries, map[string]any{
			"group_ref":    g.Ref.ExternalID,
			"display_name": g.DisplayName,
			"feed_locator": json.RawMessage(locator),
			"observed_at":  g.ObservedAt.Format(time.RFC3339Nano),
		})
	}
	return entries
}

func publishGroup(t *testing.T, db *sql.DB, r provider.GroupReadResult) provider.GroupSnapshot {
	t.Helper()
	var s provider.GroupSnapshot
	err := inTx(db, func(tx *sql.Tx) error {
		var err error
		s, err = catalogapp.PersistGroupObservationTx(context.Background(), tx, r)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestCatalogGroupRuntimeWholeObservationAndImmutableProjection(t *testing.T) {
	owner := installedDB(t)
	ctx := context.Background()
	password := randomTestPassword(t)
	if err := database.ProvisionRuntimeRole(ctx, owner, database.RuntimeRoleConfig{Role: runtimeRoleTestName, Password: password}); err != nil {
		t.Fatal(err)
	}
	runtime := openRuntimeConnection(t, password)
	defer runtime.Close()
	result := groupResult()
	snapshot := publishGroup(t, runtime, result)
	err := inTx(runtime, func(tx *sql.Tx) error {
		o, err := catalogapp.ReadGroupObservationTx(ctx, tx, snapshot.ID)
		if err != nil {
			return err
		}
		if !o.Authoritative() || o.Groups[1].Available.Value == nil || *o.Groups[1].Available.Value {
			return fmt.Errorf("lost known false: %+v", o)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`UPDATE provider_group_snapshots SET source_version='tampered'`,
		`UPDATE provider_group_snapshots SET canonical_observation=NULL,observation_digest=NULL`,
		`UPDATE provider_group_snapshots SET entries='[]'::jsonb`,
		`UPDATE provider_group_snapshots SET entries=entries || jsonb_build_object('group_ref','999','display_name','extra','feed_locator','{}'::jsonb,'observed_at',CURRENT_TIMESTAMP)`,
		`INSERT INTO provider_group_snapshots(id,provider,catalog_entry_ref,observed_at,schema_version,group_count,version) VALUES ('raw','mikan','123',CURRENT_TIMESTAMP,'test',0,9)`,
		`TRUNCATE provider_group_snapshots CASCADE`,
	} {
		if _, err := runtime.ExecContext(ctx, stmt); sqlState(err) != "42501" {
			t.Errorf("runtime bypass: %s: %v", stmt, err)
		}
	}
	if _, err := owner.ExecContext(ctx, `UPDATE provider_group_snapshots SET source_version='tampered'`); adoptAcceptPGMessage(err) != "GROUP_OBSERVATION_IMMUTABLE" {
		t.Fatalf("owner body rewrite: %v", err)
	}
	if _, err := runtime.ExecContext(ctx, `UPDATE provider_group_snapshots SET retention_until=CURRENT_TIMESTAMP`); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ExecContext(ctx, `DELETE FROM provider_group_snapshots WHERE id=$1`, snapshot.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := owner.QueryRowContext(ctx, `SELECT count(*) FROM provider_group_snapshots`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("parent deletion left snapshots: %d %v", count, err)
	}
}

func TestCatalogGroupWholeObservationFailureRollsBack(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	result := groupResult()
	snapshot := provider.GroupSnapshot{ID: "snapshot", Provider: result.Provider, CatalogEntryRef: result.CatalogEntryRef, ObservedAt: result.ObservedAt, SchemaVersion: result.SchemaVersion, Version: 1, Groups: result.Groups}
	o, err := catalogdomain.FreezeGroupObservation(snapshot, result)
	if err != nil {
		t.Fatal(err)
	}
	raw, digest, err := o.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	entries := make([]map[string]any, 0)
	for _, g := range result.Groups {
		entries = append(entries, map[string]any{"group_ref": g.Ref.ExternalID, "feed_locator": g.FeedLocator})
	}
	entryBytes, _ := json.Marshal(entries)
	for _, tc := range []struct {
		name      string
		mutate    func(map[string]any)
		entries   string
		badDigest bool
	}{
		{name: "wrong digest", badDigest: true},
		{name: "unknown key", mutate: func(o map[string]any) { o["extra"] = true }},
		{name: "missing key", mutate: func(o map[string]any) { delete(o, "schema_version") }},
		{name: "wrong evidence", mutate: func(o map[string]any) {
			o["completeness"].(map[string]any)["value"].(map[string]any)["evidence"] = "anibt-entry-total-v1"
		}},
		{name: "missing complete boolean", mutate: func(o map[string]any) { o["completeness"].(map[string]any)["value"].(map[string]any)["complete"] = nil }},
		{name: "duplicate group", mutate: func(o map[string]any) { g := o["groups"].([]any); g[1] = g[0] }},
		{name: "duplicate key", entries: string(entryBytes) + " "},
		{name: "empty projection", entries: `[]`},
		{name: "cross scope locator", entries: strings.Replace(string(entryBytes), `"catalog_entry_ref":"123"`, `"catalog_entry_ref":"124"`, 1)},
		{name: "duplicate locator", entries: func() string { b, _ := json.Marshal([]any{entries[0], entries[0]}); return string(b) }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, d := raw, digest
			if tc.mutate != nil {
				var v map[string]any
				_ = json.Unmarshal(raw, &v)
				tc.mutate(v)
				encoded, _ := json.Marshal(v)
				value, err := protocol.ParseStrict(encoded)
				if err != nil {
					t.Fatal(err)
				}
				b = protocol.Canonical(value)
				d = protocol.Digest(catalogdomain.GroupObservationDigestDomain, b)
			}
			if tc.badDigest {
				d = acqDigest("f")
			}
			if tc.name == "duplicate key" {
				b = []byte(strings.Replace(string(b), `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1))
				d = protocol.Digest(catalogdomain.GroupObservationDigestDomain, b)
			}
			projection := string(entryBytes)
			if tc.entries != "" {
				projection = tc.entries
			}
			_, err := db.ExecContext(ctx, `SELECT publish_group_observation($1,$2,$3::jsonb)`, string(b), d, projection)
			if err == nil {
				t.Fatal("invalid observation accepted")
			}
			var count int
			if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM provider_group_snapshots)+(SELECT count(*) FROM provider_group_refresh_health)`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("partial graph left: %d %v", count, err)
			}
		})
	}
}

func TestCatalogGroupObservationPreservesUnavailableAndEmpty(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name          string
		mutate        func(*provider.GroupReadResult)
		authoritative bool
	}{
		{"incomplete", func(r *provider.GroupReadResult) { r.Complete = false }, false},
		{"unregistered evidence", func(r *provider.GroupReadResult) { r.CompletenessEvidence = "" }, false},
		{"missing status", func(r *provider.GroupReadResult) { r.Groups[0].Active = nil }, false},
		{"complete empty", func(r *provider.GroupReadResult) { r.Groups = nil }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := groupResult()
			tc.mutate(&r)
			s := publishGroup(t, db, r)
			if err := inTx(db, func(tx *sql.Tx) error {
				o, err := catalogapp.ReadGroupObservationTx(ctx, tx, s.ID)
				if err != nil {
					return err
				}
				if o.Authoritative() != tc.authoritative {
					return fmt.Errorf("authority lost: %+v", o)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCatalogGroupParentShareLocksWholeObservationRetention(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	snapshot := publishGroup(t, db, groupResult())
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var digest string
	if err := tx.QueryRowContext(ctx, `SELECT observation_digest FROM provider_group_snapshots WHERE id=$1 FOR SHARE`, snapshot.ID).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var pid int
	if err := conn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := conn.ExecContext(callCtx, `DELETE FROM provider_group_snapshots WHERE id=$1`, snapshot.ID)
		done <- err
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := db.QueryRowContext(callCtx, `SELECT cardinality(pg_blocking_pids($1))>0`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("janitor did not wait for parent share: %v", err)
		case <-callCtx.Done():
			t.Fatal("janitor never reached parent lock")
		case <-ticker.C:
		}
	}
	// Exact content and its full projection remain readable while GC waits.
	o, err := catalogapp.ReadGroupObservationTx(ctx, tx, snapshot.ID)
	if err != nil || !o.Authoritative() {
		t.Fatalf("locked capsule changed: %+v %v", o, err)
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT jsonb_array_length(entries) FROM provider_group_snapshots WHERE id=$1`, snapshot.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("locked projection changed: %d %v", count, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM provider_group_snapshots WHERE id=$1`, snapshot.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("GC failed to cascade: %d %v", count, err)
	}
}

func TestCatalogGroupFinalSealRejectsPartialOwnerGraph(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	for _, tc := range []struct{ name, statement, message string }{
		{"missing entry", `INSERT INTO provider_group_snapshots(id,provider,catalog_entry_ref,observed_at,schema_version,group_count,version) VALUES ('missing-entry','mikan','123',CURRENT_TIMESTAMP,'fixture',1,1)`, "GROUP_OBSERVATION_INCOMPLETE_PROJECTION"},
		{"projection without header", `INSERT INTO provider_group_snapshots(id,provider,catalog_entry_ref,observed_at,schema_version,group_count,version,entries) VALUES ('orphan-entry','mikan','123',CURRENT_TIMESTAMP,'fixture',0,1,'[{"group_ref":"999","display_name":"extra","feed_locator":{},"observed_at":"2026-01-01T00:00:00Z"}]'::jsonb)`, "GROUP_OBSERVATION_INCOMPLETE_PROJECTION"},
		{"insert then delete", `INSERT INTO provider_group_snapshots(id,provider,catalog_entry_ref,observed_at,schema_version,group_count,version) VALUES ('deleted','mikan','123',CURRENT_TIMESTAMP,'fixture',0,1); DELETE FROM provider_group_snapshots WHERE id='deleted'`, "GROUP_OBSERVATION_FINAL_MISSING"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := inTx(db, func(tx *sql.Tx) error { _, err := tx.ExecContext(ctx, tc.statement); return err })
			if sqlState(err) != "23514" || adoptAcceptPGMessage(err) != tc.message {
				t.Fatalf("partial final graph: %v", err)
			}
		})
	}
	// A header cannot claim another canonical identity, even under owner DML.
	r := groupResult()
	snapshot := provider.GroupSnapshot{ID: "canonical-id", Provider: r.Provider, CatalogEntryRef: r.CatalogEntryRef, ObservedAt: r.ObservedAt, SchemaVersion: r.SchemaVersion, Version: 1, Groups: r.Groups}
	o, err := catalogdomain.FreezeGroupObservation(snapshot, r)
	if err != nil {
		t.Fatal(err)
	}
	canonical, digest, err := o.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	err = inTx(db, func(tx *sql.Tx) error {
		entries, err := json.Marshal(groupEntryProjection(r))
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO provider_group_snapshots(id,provider,catalog_entry_ref,observed_at,schema_version,group_count,version,canonical_observation,observation_digest,entries) VALUES ('other-id','mikan','123',$1,$2,2,1,$3,$4,$5::jsonb)`, r.ObservedAt, r.SchemaVersion, string(canonical), digest, entries); err != nil {
			return err
		}
		return nil
	})
	if sqlState(err) != "23514" || adoptAcceptPGMessage(err) != "GROUP_OBSERVATION_HEADER_MISMATCH" {
		t.Fatalf("canonical/header mismatch: %v", err)
	}
	// A literal backslash-u sequence is display content, not a decoded control.
	r = groupResult()
	r.Groups[0].DisplayName = `字面 \u0010 and "quotes"`
	s := publishGroup(t, db, r)
	if err := inTx(db, func(tx *sql.Tx) error {
		o, err := catalogapp.ReadGroupObservationTx(ctx, tx, s.ID)
		if err != nil {
			return err
		}
		if o.Groups[1].DisplayName != r.Groups[0].DisplayName {
			return errors.New("literal escaped text changed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
