package application

import (
	protocol "bansoko/internal/acquisition/terminal/domain"
	"bansoko/internal/acquisition/torrent"
	draft "bansoko/internal/anime/draft/application"
	catalog "bansoko/internal/catalog/domain"
	"bansoko/internal/persistence/txscope"
	"context"
	"database/sql"
	"errors"
	"testing"
)

func checkDirectReleasePostgres(t *testing.T, ctx context.Context, owner, runtime *sql.DB, revisions *draft.Service) {
	t.Helper()
	payload := []byte("d4:infod6:lengthi1000e4:name8:ep01.mkvee")
	meta, err := torrent.Parse(payload)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(runtime, revisions, 200000, 50)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// The manual retry respects the metadata hold like the automatic entry.
	if _, err := tx.ExecContext(ctx, `UPDATE anime_metadata SET bgm_work_id='424242',tmdb_work_id='99',tmdb_media_kind='tv',tmdb_season=1,version=version+1 WHERE managed_anime_id='anime-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE releases SET raw_facts=$1 WHERE id='rel-1'`, []byte(`{"title":"[Group] Show - 01 [1080p]","download_url":"https://example.test/direct.torrent"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO release_torrent_inputs(id,release_id,payload,payload_digest,source_url_digest,filename,parser_version,torrent_version,info_hash_v1,payload_size_bytes) VALUES('direct-input','rel-1',$1,$2,$3,'ep01.mkv','torrent-bencode-v1','v1',$4,$5)`, payload, catalog.DigestBytes(payload), catalog.DigestBytes([]byte("locator")), meta.InfoHashV1, len(payload)); err != nil {
		t.Fatal(err)
	}
	var before, after int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM releases WHERE id='rel-1'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE release_torrent_inputs SET payload=payload,payload_digest=payload_digest,filename=filename,source_url_digest=$1 WHERE id='direct-input'`, catalog.DigestBytes([]byte("other locator"))); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT version FROM releases WHERE id='rel-1'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("non-identity or equal identity update bumped version")
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT input_delete_check; DELETE FROM release_torrent_inputs WHERE id='direct-input'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `SET CONSTRAINTS release_torrent_input_delete_parent IMMEDIATE`); err == nil {
		t.Fatal("input-only delete allowed")
	}
	if _, err := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT input_delete_check`); err != nil {
		t.Fatal(err)
	}
	checkEntryAdaptersPostgres(t, ctx, tx, runtime, revisions, before)
	req := qualificationRequest{entryMode: "subscription_direct", configKind: "owner", catalogKind: "release_input", releaseID: "rel-1", requestHash: catalog.DigestBytes([]byte("direct"))}
	c, err := captureQualificationTx(ctx, tx, revisions, req)
	if err != nil {
		t.Fatal(err)
	}
	intent := serviceIntentFixture(t, c)
	intent.capture = req
	e, err := evaluateAdmission(ctx, c, intent)
	if err != nil {
		t.Fatal(err)
	}
	// anime.group is the release's source group display name, the same fact every
	// naming render reads back from rss_sources. The column is NOT NULL with a
	// non-blank CHECK, so "a source with no display name" is not a state this
	// schema admits and the projection has no absent case to fall back to.
	if got := e.input.Object["source"].Object["group"]; got.Kind != protocol.KindObject || got.Object["state"].String != string(protocol.FVKnown) || got.Object["value"].String != "G" {
		t.Fatalf("frozen source.group = %+v, want the source row's display name G", got)
	}
	q, err := protocol.ParseStrict(c.qualification.Canonical())
	if err != nil {
		t.Fatal(err)
	}
	if q.Object["feed"].Kind != protocol.KindNull || q.Object["observation"].Kind != protocol.KindNull {
		t.Fatal("direct fake capsule")
	}
	a := acceptAccess{scope: "deployment_owner_v1"}
	r := requestIdentity{namespace: "manual_acquire_v1", key: "direct-key", hash: req.requestHash, canonical: `{"command":"direct"}`}
	p, err := prepareAdmission(c, e, intent, a, r, testWitnessLimits())
	if err != nil {
		t.Fatal(err)
	}
	if p.args.PreparationMode.String != "none" || p.args.PreparationWitness.Valid {
		t.Fatal("direct fake witness")
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT direct_accept_success; UPDATE releases SET verdict='accepted_pending_download' WHERE id='rel-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AcceptNativeTx(ctx, txscope.Wrap(tx), p); err != nil {
		t.Fatalf("direct accept positive: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT direct_accept_success`); err != nil {
		t.Fatal(err)
	}
	// The stored name is the only thing that decides the fact: rss_sources.group_name
	// is NOT NULL with a non-blank CHECK, so a source that records no display name is
	// not a row this schema admits and there is no absent arm to reach through it.
	// Initial capture validates bytes; acceptance must also reject a writer that
	// updates torrent-input identity without advancing Release.version.
	if _, err := tx.ExecContext(ctx, `UPDATE release_torrent_inputs SET payload_digest=$1 WHERE id='direct-input'`, catalog.DigestBytes([]byte("changed"))); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE releases SET verdict='accepted_pending_download' WHERE id='rel-1'`); err != nil {
		t.Fatal(err)
	}
	_, err = service.AcceptNativeTx(ctx, txscope.Wrap(tx), p)
	if err == nil {
		t.Fatal("changed direct payload accepted after capture")
	}
	var failure *AcceptanceError
	if !errors.As(err, &failure) || failure.Code != "INPUT_STALE" {
		t.Fatalf("stale input error: %v", err)
	}
}
