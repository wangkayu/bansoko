package application

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// RestoreCutover pins the database catalog identities for a verified empty-DB
// restore. The caller must persist it before calling ReconcileRestoreCutover
// and retain the operation lock throughout. Names alone cannot authorize a
// rename: a same-named database may have been recreated since the checkpoint.
type RestoreCutover struct {
	Live     string `json:"live"`
	Ready    string `json:"ready"`
	Aside    string `json:"aside"`
	LiveOID  uint32 `json:"live_oid"`
	ReadyOID uint32 `json:"ready_oid"`
	OwnerOID uint32 `json:"owner_oid"`
}

// LiveDatabase pins the running deployment database before any backup,
// migration or rename. The operation later re-proves the exact OID, owner and
// installed instance instead of trusting the database name.
type LiveDatabase struct {
	Name      string `json:"name"`
	OID       uint32 `json:"oid"`
	OwnerOID  uint32 `json:"owner_oid"`
	InstallID string `json:"install_id"`
}

// restoreClaimPrefix marks the ready database an operation created for itself.
// A pre-existing database with the same generated name is never rewritten:
// without this claim it belongs to somebody else.
const restoreClaimPrefix = "bansoko.restore-claim:"

// validRestoreDatabaseName accepts PostgreSQL identifiers when quoted.
func validRestoreDatabaseName(name string) bool {
	if name == "" || len(name) > 63 || !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
		return false
	}
	switch name {
	case maintenanceDatabase, "template0", "template1":
		return false
	}
	return true
}

func (p RestoreCutover) validNames() bool {
	for _, name := range []string{p.Live, p.Ready, p.Aside} {
		if !validRestoreDatabaseName(name) {
			return false
		}
	}
	return p.Live != p.Ready && p.Live != p.Aside && p.Ready != p.Aside
}

type catalogDatabase struct {
	oid, owner        uint32
	allowsConnections bool
}

func readCatalogDatabase(ctx context.Context, db *sql.DB, name string) (catalogDatabase, error) {
	var info catalogDatabase
	err := db.QueryRowContext(ctx, `SELECT oid::bigint, datdba::bigint, datallowconn FROM pg_catalog.pg_database WHERE datname = $1`, name).Scan(&info.oid, &info.owner, &info.allowsConnections)
	if errors.Is(err, sql.ErrNoRows) {
		return catalogDatabase{}, nil
	}
	return info, err
}

func restoreOwner(ctx context.Context, admin *sql.DB) (uint32, error) {
	var oid uint32
	if err := admin.QueryRowContext(ctx, `
SELECT oid::bigint FROM pg_catalog.pg_roles
WHERE rolname = current_user AND current_user = session_user
  AND current_database() = 'postgres'
  AND current_setting('server_version_num')::int BETWEEN 170000 AND 179999
  AND (rolsuper OR (rolcreatedb AND rolcreaterole))`).Scan(&oid); err != nil {
		return 0, errors.New("restore requires a PostgreSQL 17 owner connection to postgres with CREATEDB and CREATEROLE")
	}
	return oid, nil
}

// canTerminateForeignSessions proves the maintenance role may end the serving
// role's sessions. The restore renames the live database, which PostgreSQL
// only allows once no other session is attached; a role that cannot terminate
// them would discover that after the backup and the migration.
func canTerminateForeignSessions(ctx context.Context, admin *sql.DB) (bool, error) {
	var allowed bool
	if err := admin.QueryRowContext(ctx, `
SELECT r.rolsuper OR COALESCE(pg_has_role(r.oid, 'pg_signal_backend', 'MEMBER'), false)
FROM pg_catalog.pg_roles r WHERE r.rolname = current_user`).Scan(&allowed); err != nil {
		return false, errors.New("cannot read the maintenance role's session signal privileges")
	}
	return allowed, nil
}

// CheckRestoreConnection verifies that the maintenance connection can replay a
// restore even when the live database name is temporarily absent.
func CheckRestoreConnection(ctx context.Context, admin *sql.DB) error {
	_, err := restoreOwner(ctx, admin)
	return err
}

// CheckRestorePrerequisites is the read-only admission every takeover must pass
// before it backs up or migrates anything. It proves the PostgreSQL version,
// the maintenance role's database-creation and rename privileges, its ability
// to evict the serving sessions, the live database's exact owner, and that the
// database name is one PostgreSQL accepts.
func CheckRestorePrerequisites(ctx context.Context, admin *sql.DB, live string) (LiveDatabase, error) {
	if !validRestoreDatabaseName(live) {
		return LiveDatabase{}, errors.New("the deployment database name is not a valid PostgreSQL identifier")
	}
	owner, err := restoreOwner(ctx, admin)
	if err != nil {
		return LiveDatabase{}, err
	}
	allowed, err := canTerminateForeignSessions(ctx, admin)
	if err != nil {
		return LiveDatabase{}, err
	}
	if !allowed {
		return LiveDatabase{}, errors.New("restore requires the maintenance role to terminate serving sessions (superuser or pg_signal_backend)")
	}
	info, err := readCatalogDatabase(ctx, admin, live)
	if err != nil {
		return LiveDatabase{}, errors.New("cannot read the deployment database identity")
	}
	if info.oid == 0 || info.owner != owner || !info.allowsConnections {
		return LiveDatabase{}, errors.New("restore requires a live database owned by the maintenance role")
	}
	return LiveDatabase{Name: live, OID: info.oid, OwnerOID: owner}, nil
}

// ProveLiveDatabase re-proves a pinned deployment database identity immediately
// before an irreversible effect. A replaced database with the same name and
// owner is still a different database and is refused.
func ProveLiveDatabase(ctx context.Context, admin *sql.DB, pinned LiveDatabase) error {
	if !validRestoreDatabaseName(pinned.Name) || pinned.OID == 0 || pinned.OwnerOID == 0 {
		return errors.New("the deployment database identity was never pinned")
	}
	owner, err := restoreOwner(ctx, admin)
	if err != nil {
		return err
	}
	if owner != pinned.OwnerOID {
		return errors.New("the maintenance role is no longer the pinned database owner")
	}
	info, err := readCatalogDatabase(ctx, admin, pinned.Name)
	if err != nil {
		return errors.New("cannot re-read the deployment database identity")
	}
	if info.oid != pinned.OID || info.owner != pinned.OwnerOID || !info.allowsConnections {
		return errors.New("the deployment database is not the exact pinned database")
	}
	return nil
}

// CreateEmptyRestoreDatabase creates an empty database using the live
// deployment's locale and encoding. The caller owns the name and is
// responsible for restoring and verifying it before pinning the cutover.
func CreateEmptyRestoreDatabase(ctx context.Context, admin *sql.DB, live, ready string) error {
	p := RestoreCutover{Live: live, Ready: ready, Aside: "restore_reserved_validation_name"}
	if !p.validNames() {
		return errors.New("invalid database names for empty restore")
	}
	owner, err := restoreOwner(ctx, admin)
	if err != nil {
		return err
	}
	info, err := readCatalogDatabase(ctx, admin, live)
	if err != nil || info.oid == 0 || info.owner != owner || !info.allowsConnections {
		return errors.New("empty restore requires an owned live database")
	}
	return createLike(ctx, admin, ready, live)
}

// PinRestoreCutover is read-only. Call it after loading and verifying Ready,
// before the first rename. It refuses collisions instead of choosing another
// name: reusing an operation's name without durable evidence is unsafe.
func PinRestoreCutover(ctx context.Context, admin *sql.DB, live, ready, aside, operationID string) (RestoreCutover, error) {
	p := RestoreCutover{Live: live, Ready: ready, Aside: aside}
	if !p.validNames() || operationID == "" {
		return RestoreCutover{}, errors.New("invalid database names for restore cutover")
	}
	owner, err := restoreOwner(ctx, admin)
	if err != nil {
		return RestoreCutover{}, err
	}
	a, b, c, err := p.catalog(ctx, admin)
	if err != nil {
		return RestoreCutover{}, err
	}
	if a.oid == 0 || b.oid == 0 || c.oid != 0 || a.owner != owner || b.owner != owner || !a.allowsConnections || !b.allowsConnections {
		return RestoreCutover{}, errors.New("restore cutover requires two distinct owned databases and an unused aside name")
	}
	if claim, err := databaseClaimByOID(ctx, admin, b.oid); err != nil || claim != operationID {
		return RestoreCutover{}, errors.New("the ready database carries no claim from this operation")
	}
	p.LiveOID, p.ReadyOID, p.OwnerOID = a.oid, b.oid, owner
	return p, nil
}

func (p RestoreCutover) catalog(ctx context.Context, admin *sql.DB) (live, ready, aside catalogDatabase, err error) {
	for i, name := range []string{p.Live, p.Ready, p.Aside} {
		info, err := readCatalogDatabase(ctx, admin, name)
		if err != nil {
			return live, ready, aside, errors.New("cannot read database identities for restore cutover")
		}
		*[]*catalogDatabase{&live, &ready, &aside}[i] = info
	}
	return live, ready, aside, nil
}

// ReconcileRestoreCutover replays a previously pinned cutover, including the
// window where the live name is absent. An unexpected name or OID stops the
// operation without reclaiming or modifying foreign databases. The old DB is
// retained with connections disabled; disposal is a separate effect.
func ReconcileRestoreCutover(ctx context.Context, admin *sql.DB, p RestoreCutover, operationID string, proveLive func() error, onRename func(string)) error {
	if !p.validNames() || operationID == "" || p.LiveOID == 0 || p.ReadyOID == 0 || p.LiveOID == p.ReadyOID || p.OwnerOID == 0 {
		return errors.New("invalid pinned restore cutover")
	}
	owner, err := restoreOwner(ctx, admin)
	if err != nil || owner != p.OwnerOID {
		return errors.New("restore cutover maintenance role does not match the pinned owner")
	}
	for range 3 {
		complete, err := advanceRestoreCutover(ctx, admin, p, operationID, proveLive, onRename)
		if complete || err != nil {
			return err
		}
	}
	return errors.New("restore cutover did not converge")
}

func advanceRestoreCutover(ctx context.Context, admin *sql.DB, p RestoreCutover, operationID string, proveLive func() error, onRename func(string)) (bool, error) {
	live, ready, aside, err := p.catalog(ctx, admin)
	if err != nil {
		return false, err
	}
	owned := func(db catalogDatabase, oid uint32) bool { return db.oid == oid && db.owner == p.OwnerOID }
	restored, found, err := p.restoredDatabase(ctx, admin)
	if err != nil {
		return false, err
	}
	if !found {
		return false, errors.New("the operation's restored database is no longer present")
	}
	if claim, err := databaseClaimByOID(ctx, admin, restored.oid); err != nil || claim != operationID {
		return false, errors.New("the restored database carries no claim from this operation")
	}
	switch {
	case owned(live, p.LiveOID) && owned(ready, p.ReadyOID) && aside.oid == 0:
		if proveLive != nil {
			if err := proveLive(); err != nil {
				return false, err
			}
		}
		if err := renameQuarantinedDatabase(ctx, admin, p.Live, p.Aside); err != nil {
			return false, err
		}
		if onRename != nil {
			onRename("restore_live_quarantined")
		}
		return false, nil
	case live.oid == 0 && owned(ready, p.ReadyOID) && owned(aside, p.LiveOID) && !aside.allowsConnections:
		if err := renameQuarantinedDatabase(ctx, admin, p.Ready, p.Live); err != nil {
			return false, err
		}
		if onRename != nil {
			onRename("restore_ready_renamed")
		}
		return false, nil
	case owned(live, p.ReadyOID) && ready.oid == 0 && owned(aside, p.LiveOID) && !aside.allowsConnections:
		if !live.allowsConnections {
			if _, err := admin.ExecContext(ctx, `ALTER DATABASE `+quoteIdent(p.Live)+` ALLOW_CONNECTIONS true`); err != nil {
				return false, fmt.Errorf("enable connections to restored database: %w", err)
			}
		}
		return true, nil
	default:
		return false, errors.New("database identities do not match the pinned restore cutover")
	}
}

// restoredDatabase finds the database holding the restored bytes regardless of
// the name a crash left it under.
func (p RestoreCutover) restoredDatabase(ctx context.Context, admin *sql.DB) (catalogDatabase, bool, error) {
	var info catalogDatabase
	err := admin.QueryRowContext(ctx, `SELECT oid::bigint, datdba::bigint, datallowconn FROM pg_catalog.pg_database WHERE oid = $1`, p.ReadyOID).
		Scan(&info.oid, &info.owner, &info.allowsConnections)
	if errors.Is(err, sql.ErrNoRows) {
		return catalogDatabase{}, false, nil
	}
	if err != nil {
		return catalogDatabase{}, false, errors.New("cannot read the restored database identity")
	}
	return info, true, nil
}

// Commit the quarantine and rename together so a process stop cannot hide the
// live fence behind ALLOW_CONNECTIONS=false before the first rename.
func renameQuarantinedDatabase(ctx context.Context, admin *sql.DB, database, target string) error {
	tx, err := admin.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `ALTER DATABASE `+quoteIdent(database)+` ALLOW_CONNECTIONS false`); err != nil {
		return fmt.Errorf("stop new connections for restore cutover: %w", err)
	}
	if err := terminateRestoreConnections(ctx, tx, database); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `ALTER DATABASE `+quoteIdent(database)+` RENAME TO `+quoteIdent(target)); err != nil {
		return err
	}
	return tx.Commit()
}

func terminateRestoreConnections(ctx context.Context, tx *sql.Tx, database string) error {
	rows, err := tx.QueryContext(ctx, `SELECT pg_catalog.pg_terminate_backend(pid, 5000)
FROM pg_catalog.pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, database)
	if err != nil {
		return fmt.Errorf("terminate existing connections for restore cutover: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var terminated bool
		if err := rows.Scan(&terminated); err != nil || !terminated {
			return errors.New("existing database connection did not terminate before restore cutover")
		}
	}
	return rows.Err()
}

// EnsureOperationRestoreDatabase admits an operation-reserved ready name while
// the live database is still in place. A name this operation did not create is
// never reused: it may belong to another database or another operation.
func EnsureOperationRestoreDatabase(ctx context.Context, admin *sql.DB, live, ready, aside, operationID string) error {
	p := RestoreCutover{Live: live, Ready: ready, Aside: aside}
	if !p.validNames() || operationID == "" {
		return errors.New("invalid operation restore database names")
	}
	owner, err := restoreOwner(ctx, admin)
	if err != nil {
		return err
	}
	a, b, c, err := p.catalog(ctx, admin)
	if err != nil {
		return err
	}
	if a.oid == 0 || a.owner != owner || !a.allowsConnections || c.oid != 0 {
		return errors.New("restore requires an owned live database and an unused aside name")
	}
	if b.oid == 0 {
		return createClaimedRestoreDatabase(ctx, admin, live, ready, operationID)
	}
	if b.owner != owner || !b.allowsConnections {
		return errors.New("operation restore database has unexpected catalog identity")
	}
	claim, err := databaseClaimByOID(ctx, admin, b.oid)
	if err != nil {
		return err
	}
	if claim != operationID {
		return errors.New("a pre-existing database already uses the operation's ready name")
	}
	return nil
}

func createClaimedRestoreDatabase(ctx context.Context, admin *sql.DB, live, ready, operationID string) error {
	// CREATE DATABASE cannot run in a transaction. A crash here leaves only
	// an inaccessible scratch DB, never an unclaimed canonical ready name.
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	scratch := fmt.Sprintf("bsk_restore_unclaimed_%x", nonce)
	if err := createLikeWithConnections(ctx, admin, scratch, live, false); err != nil {
		return err
	}
	tx, err := admin.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `ALTER DATABASE `+quoteIdent(scratch)+` RENAME TO `+quoteIdent(ready)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `COMMENT ON DATABASE `+quoteIdent(ready)+` IS `+quoteLiteral(restoreClaimPrefix+operationID)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `ALTER DATABASE `+quoteIdent(ready)+` ALLOW_CONNECTIONS true`); err != nil {
		return err
	}
	return tx.Commit()
}

func databaseClaimByOID(ctx context.Context, admin *sql.DB, oid uint32) (string, error) {
	var comment sql.NullString
	if err := admin.QueryRowContext(ctx, `SELECT pg_catalog.shobj_description(oid, 'pg_database') FROM pg_catalog.pg_database WHERE oid = $1`, oid).Scan(&comment); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", errors.New("cannot read the restore ownership claim")
	}
	return readRestoreClaim(comment.String), nil
}

func readRestoreClaim(comment string) string {
	if after, ok := strings.CutPrefix(comment, restoreClaimPrefix); ok {
		return after
	}
	return ""
}
