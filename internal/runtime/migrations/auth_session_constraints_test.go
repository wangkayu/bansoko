package migrations

import "testing"

// The bootstrap singleton is one row, and a ready instance records when it
// completed.
func TestInstanceBootstrapConstraints(t *testing.T) {
	p := newConstraintProbe(t)
	var state, singletonCreatedAt string
	if err := p.db.QueryRowContext(p.ctx, `
SELECT state, created_at::text
FROM instance_bootstrap WHERE singleton_id = 1`).Scan(&state, &singletonCreatedAt); err != nil {
		t.Fatal(err)
	}
	if state != "uninitialized" || singletonCreatedAt == "" {
		t.Fatalf("unexpected bootstrap singleton: state=%q created_at=%q", state, singletonCreatedAt)
	}
	p.reject("second bootstrap singleton row",
		`INSERT INTO instance_bootstrap(singleton_id, state, install_id) VALUES (2, 'uninitialized', 'other-install')`)
	p.reject("ready bootstrap without completed_at",
		`UPDATE instance_bootstrap SET state = 'ready' WHERE singleton_id = 1`)
	p.reject("retired recovery bootstrap state",
		`UPDATE instance_bootstrap SET state = 'recovery_required' WHERE singleton_id = 1`)
}

// auth_sessions holds only browser sessions bound to a local account, with a
// globally unique token hash.
func TestAuthSessionConstraints(t *testing.T) {
	p := newConstraintProbe(t)
	p.accept("account", `
		INSERT INTO local_accounts(id, username, password_hash)
		VALUES ('acct-1', 'owner-probe', repeat('x', 60))`)
	p.accept("user session", `
		INSERT INTO auth_sessions(id, account_id, token_hash, expires_at)
		VALUES ('session-1', 'acct-1', repeat('c', 64), CURRENT_TIMESTAMP + INTERVAL '24 hours')`)
	p.reject("session without an account",
		`INSERT INTO auth_sessions(id, token_hash, expires_at)
		 VALUES ('session-orphan', repeat('d', 64), CURRENT_TIMESTAMP + INTERVAL '24 hours')`)
	p.reject("session for an unknown account",
		`INSERT INTO auth_sessions(id, account_id, token_hash, expires_at)
		 VALUES ('session-unknown', 'acct-missing', repeat('e', 64), CURRENT_TIMESTAMP + INTERVAL '24 hours')`)
	p.reject("session expiring before creation",
		`INSERT INTO auth_sessions(id, account_id, token_hash, expires_at)
		 VALUES ('session-expired', 'acct-1', repeat('f', 64), CURRENT_TIMESTAMP - INTERVAL '1 minute')`)
	p.reject("duplicate token hash",
		`INSERT INTO auth_sessions(id, account_id, token_hash, expires_at)
		 VALUES ('session-duplicate', 'acct-1', repeat('c', 64), CURRENT_TIMESTAMP + INTERVAL '24 hours')`)
	p.reject("retired setup bootstrap state",
		`UPDATE instance_bootstrap SET state = 'setup' WHERE singleton_id = 1`)
}
