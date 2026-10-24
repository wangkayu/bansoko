package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRotateSecretKeyRequiresEnvironment(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"no database": {"BANSOKO_SECRET_ENCRYPTION_KEY": strings.Repeat("a", 32), "BANSOKO_NEW_SECRET_ENCRYPTION_KEY": strings.Repeat("b", 32)},
		"no new key":  {"BANSOKO_DATABASE_URL": "postgres://x", "BANSOKO_SECRET_ENCRYPTION_KEY": strings.Repeat("a", 32)},
		"short key":   {"BANSOKO_DATABASE_URL": "postgres://x", "BANSOKO_SECRET_ENCRYPTION_KEY": "short", "BANSOKO_NEW_SECRET_ENCRYPTION_KEY": strings.Repeat("b", 32)},
	} {
		var stdout, stderr bytes.Buffer
		code := runRotateSecretKey(context.Background(), func(key string) string { return env[key] }, &stdout, &stderr)
		if code != 2 || stdout.Len() != 0 {
			t.Fatalf("%s: exit %d, stdout %q", name, code, stdout.String())
		}
		for _, value := range env {
			if len(value) >= 32 && strings.Contains(stderr.String(), value) {
				t.Fatalf("%s: the message printed a key", name)
			}
		}
	}
}
