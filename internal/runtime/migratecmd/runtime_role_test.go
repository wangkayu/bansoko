package migratecmd

import "testing"

func TestRuntimeRoleFromEnv(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	role, err := runtimeRoleFromEnv(env(map[string]string{
		"BANSOKO_RUNTIME_DATABASE_URL": "postgres://bansoko_app:p%40ss%2Fword-0123456789abcdef@db:5432/bansoko?sslmode=disable",
	}), "production")
	if err != nil || role == nil || role.Role != "bansoko_app" || role.Password != "p@ss/word-0123456789abcdef" {
		t.Fatalf("runtime role = %+v err=%v", role, err)
	}
	if role, err := runtimeRoleFromEnv(env(nil), "development"); err != nil || role != nil {
		t.Fatalf("development without a runtime connection = %+v %v, want no provisioning", role, err)
	}
	if _, err := runtimeRoleFromEnv(env(nil), "production"); err == nil {
		t.Fatal("production accepted a missing runtime connection")
	}
	if _, err := runtimeRoleFromEnv(env(map[string]string{"BANSOKO_RUNTIME_DATABASE_URL": "postgres://db/bansoko"}), "test"); err == nil {
		t.Fatal("a runtime connection without credentials was accepted")
	}
}

func TestOwnerDSNPrefersTheOwnerConnection(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	if got := ownerDSN(env(map[string]string{"BANSOKO_DATABASE_URL": "postgres://owner@db/bansoko"}), "postgres://bansoko_app:x@db/bansoko"); got != "postgres://owner@db/bansoko" {
		t.Fatalf("ownerDSN = %q, want the owner connection", got)
	}
	if got := ownerDSN(env(map[string]string{"BANSOKO_DATABASE_URL": "  "}), "postgres://flag@db/bansoko"); got != "postgres://flag@db/bansoko" {
		t.Fatalf("ownerDSN = %q, want the flag fallback", got)
	}
}
