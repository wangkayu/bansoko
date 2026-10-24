package database

import (
	"context"
	"errors"
	"os"
	"testing"
)

// Open requires a DSN, applies sane pool defaults, and pings the database.
// These tests run against an isolated BANSOKO_TEST_DATABASE_URL and are skipped when
// it is not configured (consistent with the other PG packages).

func TestOpenRequiresDSN(t *testing.T) {
	if _, err := Open(context.Background(), Config{}); !errors.Is(err, ErrDSNRequired) {
		t.Fatalf("expected ErrDSNRequired, got %v", err)
	}
}

func TestOpenPingsAndAppliesPoolDefaults(t *testing.T) {
	dsn := os.Getenv("BANSOKO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("BANSOKO_TEST_DATABASE_URL is not configured")
	}
	db, err := Open(context.Background(), Config{DSN: dsn})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	defer db.Close()

	// Defaults are applied when the config leaves the pool settings zeroed.
	stats := db.Stats()
	if stats.MaxOpenConnections != 10 {
		t.Fatalf("max open conns = %d, want default 10", stats.MaxOpenConnections)
	}
	// A ping was performed by Open; a live pool proves the DSN is reachable.
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("ping after open: %v", err)
	}
}

func TestOpenHonorsExplicitPoolSettings(t *testing.T) {
	dsn := os.Getenv("BANSOKO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("BANSOKO_TEST_DATABASE_URL is not configured")
	}
	db, err := Open(context.Background(), Config{DSN: dsn, MaxOpenConns: 3, MaxIdleConns: 1})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	defer db.Close()
	stats := db.Stats()
	if stats.MaxOpenConnections != 3 {
		t.Fatalf("max open conns = %d, want 3", stats.MaxOpenConnections)
	}
}
