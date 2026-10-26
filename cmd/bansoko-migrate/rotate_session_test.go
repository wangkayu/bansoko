package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRotateSessionSecretRequiresEnvironment(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"no database": {"BANSOKO_NEW_SESSION_SECRET": strings.Repeat("b", 32)},
		"short key":   {"BANSOKO_DATABASE_URL": "postgres://x", "BANSOKO_NEW_SESSION_SECRET": "short"},
	} {
		var stdout, stderr bytes.Buffer
		code := runRotateSessionSecret(context.Background(), func(key string) string { return env[key] }, &stdout, &stderr)
		if code != 2 || stdout.Len() != 0 {
			t.Fatalf("%s: exit %d, stdout %q", name, code, stdout.String())
		}
		for _, value := range env {
			if len(value) >= 32 && strings.Contains(stderr.String(), value) {
				t.Fatalf("%s: the message printed a secret", name)
			}
		}
	}
}

// TestRotateSessionSecretRefusesUnchangedSecret keeps a rotation that would
// change nothing from revoking every session for no gain.
func TestRotateSessionSecretRefusesUnchangedSecret(t *testing.T) {
	secret := strings.Repeat("s", 32)
	env := map[string]string{
		"BANSOKO_DATABASE_URL":          "postgres://x",
		"BANSOKO_SESSION_SECRET":        secret,
		"BANSOKO_NEW_SESSION_SECRET":    secret,
		"BANSOKO_SECRET_ENCRYPTION_KEY": strings.Repeat("e", 32),
		"BANSOKO_CONFIG_DIR":            t.TempDir(),
	}
	var stdout, stderr bytes.Buffer
	code := runRotateSessionSecret(context.Background(), func(key string) string { return env[key] }, &stdout, &stderr)
	if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "nothing to rotate") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
}
