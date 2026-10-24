package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"testing"
	"time"
)

func domainDigest(domain, canonical string) string {
	sum := sha256.Sum256([]byte(domain + "\x00" + canonical))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func plainDigest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

const validInput = `{"schema_version":1}`

// rootEnv is one disposable database with the baseline applied and a counter
// for unique roots.
type rootEnv struct {
	db  *sql.DB
	now time.Time
	n   int
}

func newRootEnv(t *testing.T) *rootEnv {
	t.Helper()
	db := installedDB(t)
	return &rootEnv{db: db, now: time.Now().UTC().Truncate(time.Microsecond)}
}

// root inserts a committed root whose admission digest is the digest of the given
// canonical input, and returns its id. The seed carries the queued branch's task
// and first work items.
func (e *rootEnv) root(t *testing.T, canonicalInput string) string {
	t.Helper()
	e.n++
	id := fmt.Sprintf("acq-%d", e.n)
	row := with2(validAcq(id, fmt.Sprintf("key-%d", e.n), e.now),
		map[string]any{"admission_digest": domainDigest("acquisition-input:v1", canonicalInput)})
	seedQueuedRoot(t, e.db, row)
	return id
}

// inTx runs fn and commits; a statement error or a deferred violation at commit
// is returned, and nothing is left behind either way.
func inTx(db *sql.DB, fn func(tx *sql.Tx) error) error {
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func expectState(t *testing.T, name string, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Errorf("%s must be accepted: %v", name, err)
		}
		return
	}
	if err == nil {
		t.Errorf("%s must be rejected with SQLSTATE %s", name, want)
		return
	}
	if got := sqlState(err); got != want {
		t.Errorf("%s: SQLSTATE = %q (%v), want %s", name, got, err, want)
	}
}

type setFile struct {
	ref     string
	ordinal int
	path    string
	size    int64
	kind    string
}

// declaredSet arranges one committed declared set on an already seeded root.
// The headers live on the root row now, so the writer updates acquisitions once
// every child row exists.
type declaredSet struct {
	id             string
	manifestDigest string
	files          []setFile
}

func newDeclaredSet(id string) declaredSet {
	return declaredSet{id: id, manifestDigest: acqDigest("1"), files: []setFile{
		{"file-video", 0, "Show/ep01.mkv", 1000, "video"},
		{"file-sub", 1, "Show/ep01.ass", 10, "subtitle"},
	}}
}

func (s declaredSet) write(tx *sql.Tx) error {
	ctx := context.Background()
	for _, f := range s.files {
		if _, err := tx.ExecContext(ctx, `INSERT INTO acquisition_files(acquisition_id, file_ref, ordinal, relative_path, exact_size, media_kind, identity_evidence)
VALUES ($1, $2, $3, $4, $5, $6, '{}')`, s.id, f.ref, f.ordinal, f.path, f.size, f.kind); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE acquisition_files SET requirement='excluded', requirement_reason_code='selected', selection_evidence='{}' WHERE acquisition_id=$1 AND file_ref='file-sub'`, s.id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE acquisition_files SET requirement='required', requirement_reason_code='selected', selection_evidence='{}',
 spec_digest=$2, intent='ensure_present', operation='hardlink', canonical_path='Anime/Show/S01E01.mkv', collision='block',
 target_source_cleanup='{}', render_snapshot='{}'
WHERE acquisition_id=$1 AND file_ref='file-video'`, s.id, acqDigest("3")); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE acquisitions SET manifest_digest=$2, manifest_item_count=$3, manifest_sealed_at=CURRENT_TIMESTAMP,
 requirements_manifest_digest=$2, requirements_selection_digest=$4, requirements_evidence_digest=$5, requirements_digest=$6,
 requirements_item_count=$3, requirements_required_count=1, requirements_sealed_at=CURRENT_TIMESTAMP,
 target_requirements_digest=$6, target_digest=$7, target_sealed_at=CURRENT_TIMESTAMP WHERE id=$1`,
		s.id, s.manifestDigest, len(s.files), acqDigest("5"), acqDigest("6"), acqDigest("2"), acqDigest("4"))
	return err
}
