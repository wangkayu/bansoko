package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// RuntimeGroupRole is the fixed NOLOGIN group role every deployment runtime
// login role is granted membership in. It is created by the schema baseline with the
// runtime privilege boundary; provisioning here never changes its grants.
const RuntimeGroupRole = "bansoko_runtime"

// RoleNamePattern is the strict identifier shape accepted for a configured role
// name. It is validated before any identifier-quoting DDL is built so a
// misconfigured value can never reach the server as syntax.
var RoleNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)

var (
	ErrRuntimeRoleNameInvalid     = errors.New("runtime database role name is invalid")
	ErrRuntimeRolePasswordMissing = errors.New("runtime database role password is required")
	// ErrCannotManageRoles means the owner connection lacks CREATEROLE (and is
	// not a superuser), so neither the baseline group role nor the runtime
	// login role can be created or converged.
	ErrCannotManageRoles = errors.New("the database account cannot create roles")
)

// AssertCanManageRoles fails with ErrCannotManageRoles when the connected
// account is neither a superuser nor holds CREATEROLE. Migrate calls it before
// applying the schema so an external database account without the privilege
// gets one actionable error instead of a permission failure mid-migration.
func AssertCanManageRoles(ctx context.Context, db *sql.DB) error {
	var allowed bool
	if err := db.QueryRowContext(ctx, `
SELECT rolsuper OR rolcreaterole
FROM pg_roles
WHERE rolname = current_user`).Scan(&allowed); err != nil {
		return fmt.Errorf("read role management privilege: %w", err)
	}
	if !allowed {
		return ErrCannotManageRoles
	}
	return nil
}

// RuntimeRoleConfig is the deployment-supplied runtime login role.
type RuntimeRoleConfig struct {
	// Role is the deployment's runtime login role name (from the derived
	// BANSOKO_RUNTIME_DATABASE_URL; bansoko_app in container deployments).
	Role string
	// Password is the deployment's runtime login password. It is never logged;
	// only the server-side `format('%L')` quoting sees it.
	Password string
}

// ProvisionRuntimeRole creates or idempotently converges one runtime login role
// and grants it membership in RuntimeGroupRole.
//
// The role is forced to NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS, so even
// a pre-existing role is tightened on every migrate. The password and role name
// are passed as bind parameters to `format()` for identifier/literal quoting;
// no caller value is concatenated into the statement text.
//
// Re-running it is a no-op convergence: attributes, password and group
// membership all end in the same state.
func ProvisionRuntimeRole(ctx context.Context, db *sql.DB, cfg RuntimeRoleConfig) error {
	if db == nil {
		return errors.New("runtime role provisioning requires a database")
	}
	role := strings.TrimSpace(cfg.Role)
	if !RoleNamePattern.MatchString(role) {
		return fmt.Errorf("%w: %q", ErrRuntimeRoleNameInvalid, role)
	}
	if len(cfg.Password) < 16 {
		return ErrRuntimeRolePasswordMissing
	}

	exists, err := roleExists(ctx, db, role)
	if err != nil {
		return err
	}
	verb := "CREATE ROLE"
	if exists {
		verb = "ALTER ROLE"
	}
	ddl, err := formatDDL(ctx, db,
		verb+" %I LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS INHERIT PASSWORD %L",
		role, cfg.Password)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("converge runtime role %q: %w", role, err)
	}

	if role != RuntimeGroupRole {
		if err := ensureRole(ctx, db, RuntimeGroupRole); err != nil {
			return err
		}
		grant, err := formatDDL(ctx, db, "GRANT %I TO %I", RuntimeGroupRole, role)
		if err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, grant); err != nil {
			return fmt.Errorf("grant runtime group role to %q: %w", role, err)
		}
	}
	return nil
}

// AssertRuntimeRoleSeparated fails closed when the connected role is not a safe
// runtime identity: it must not be superuser, must not bypass row-level
// security, must not own application relations in the public schema and must
// not be able to create objects there. Production startup calls it so a
// deployment that accidentally points the server at the schema owner refuses to
// serve instead of silently running with DDL/trigger privileges.
func AssertRuntimeRoleSeparated(ctx context.Context, db *sql.DB) error {
	var role string
	var isSuper, bypassRLS bool
	if err := db.QueryRowContext(ctx, `
SELECT current_user,
       rolsuper,
       rolbypassrls
FROM pg_roles
WHERE rolname = current_user`).Scan(&role, &isSuper, &bypassRLS); err != nil {
		return fmt.Errorf("read runtime role attributes: %w", err)
	}
	if isSuper {
		return fmt.Errorf("runtime database role %q is a superuser", role)
	}
	if bypassRLS {
		return fmt.Errorf("runtime database role %q can bypass row-level security", role)
	}
	var ownedRelations int
	if err := db.QueryRowContext(ctx, `
SELECT count(*)
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public'
  AND c.relkind IN ('r', 'p')
  AND pg_get_userbyid(c.relowner) = current_user`).Scan(&ownedRelations); err != nil {
		return fmt.Errorf("read runtime role relation ownership: %w", err)
	}
	if ownedRelations > 0 {
		return fmt.Errorf("runtime database role %q owns %d public relations", role, ownedRelations)
	}
	var canCreate bool
	if err := db.QueryRowContext(ctx, `SELECT has_schema_privilege(current_user, 'public', 'CREATE')`).Scan(&canCreate); err != nil {
		return fmt.Errorf("read runtime role schema privilege: %w", err)
	}
	if canCreate {
		return fmt.Errorf("runtime database role %q can create objects in the public schema", role)
	}
	return nil
}

func roleExists(ctx context.Context, db *sql.DB, role string) (bool, error) {
	var one int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM pg_roles WHERE rolname = $1`, role).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read role %q: %w", role, err)
	}
	return true, nil
}

func ensureRole(ctx context.Context, db *sql.DB, role string) error {
	if !RoleNamePattern.MatchString(role) {
		return fmt.Errorf("%w: %q", ErrRuntimeRoleNameInvalid, role)
	}
	exists, err := roleExists(ctx, db, role)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	ddl, err := formatDDL(ctx, db,
		"CREATE ROLE %I NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS",
		role)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create group role %q: %w", role, err)
	}
	return nil
}

// formatDDL asks the server to quote the identifiers and literals of one
// statement template. The result is server-quoted text, so no caller value is
// concatenated into the statement.
func formatDDL(ctx context.Context, db *sql.DB, template string, args ...any) (string, error) {
	placeholders := make([]string, len(args))
	binds := make([]any, len(args))
	for i := range args {
		// format() is variadic "any", so its parameters need an explicit type to
		// be inferred from a bind parameter.
		placeholders[i] = fmt.Sprintf("$%d::text", i+1)
		binds[i] = args[i]
	}
	statement := fmt.Sprintf("SELECT format(%s)", quoteLiteral(template))
	if len(placeholders) > 0 {
		statement = fmt.Sprintf("SELECT format(%s, %s)", quoteLiteral(template), strings.Join(placeholders, ", "))
	}
	var quoted string
	if err := db.QueryRowContext(ctx, statement, binds...).Scan(&quoted); err != nil {
		return "", fmt.Errorf("quote role DDL: %w", err)
	}
	return quoted, nil
}

// quoteLiteral renders a Go string as a standard SQL string literal for use as
// the format() template. The template is a compile-time constant, so this only
// has to be correct, not caller-controlled.
func quoteLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
