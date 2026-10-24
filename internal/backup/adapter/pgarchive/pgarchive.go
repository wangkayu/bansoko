// Package pgarchive adapts the whitelisted PostgreSQL client tooling (the
// embedded-update pgexec executor) and schema-owner connections to the backup
// ports. Connection facts travel only through PG* environment variables built
// from the DSN, never argv.
package pgarchive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"

	backupapplication "bansoko/internal/backup/application"
	"bansoko/internal/update/adapter/pgexec"
	updateapplication "bansoko/internal/update/application"
)

// Tools dumps, verifies and loads archives of the deployment database.
type Tools struct {
	executor *pgexec.Executor
	env      []string
}

// NewTools resolves pg_dump/pg_restore among the whitelisted locations and
// binds the connection facts of ownerURL. It fails when the tools are absent
// or the DSN is not URL-form.
func NewTools(ownerURL string, candidateDirs []string) (*Tools, error) {
	env, err := updateapplication.PostgresChildEnv(ownerURL)
	if err != nil {
		return nil, err
	}
	executor, err := pgexec.New(candidateDirs)
	if err != nil {
		return nil, err
	}
	return &Tools{executor: executor, env: env}, nil
}

// Dump writes a custom-format (-Fc) archive to outPath.
func (t *Tools) Dump(ctx context.Context, outPath string) error {
	return t.executor.Dump(ctx, t.env, outPath)
}

// Verify proves the archive parses (pg_restore --list) without restoring it.
func (t *Tools) Verify(ctx context.Context, path string) error {
	return t.executor.ListContents(ctx, t.env, path)
}

// RestoreInto loads the archive into the empty database in one transaction.
func (t *Tools) RestoreInto(ctx context.Context, path, database string) error {
	return t.executor.RestoreFresh(ctx, t.env, path, database)
}

// Cluster opens schema-owner connections to databases on the deployment's
// PostgreSQL server.
type Cluster struct {
	base     *url.URL
	database string
}

// NewCluster parses the owner DSN. Only URL-form DSNs naming a database are
// supported.
func NewCluster(ownerURL string) (*Cluster, error) {
	parsed, err := url.Parse(strings.TrimSpace(ownerURL))
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return nil, errors.New("restore requires a URL-form postgres DSN")
	}
	database := strings.TrimPrefix(parsed.Path, "/")
	if database == "" || strings.Contains(database, "/") {
		return nil, errors.New("postgres DSN does not name a database")
	}
	return &Cluster{base: parsed, database: database}, nil
}

// Database is the deployment database name.
func (c *Cluster) Database() string { return c.database }

// Open connects to database with the owner's credentials and options.
func (c *Cluster) Open(ctx context.Context, database string) (*sql.DB, error) {
	target := *c.base
	target.Path = "/" + database
	target.RawPath = ""
	db, err := sql.Open("pgx", target.String())
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", database, err)
	}
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect %s: %w", database, err)
	}
	return db, nil
}

var (
	_ backupapplication.Tools   = (*Tools)(nil)
	_ backupapplication.Cluster = (*Cluster)(nil)
)
