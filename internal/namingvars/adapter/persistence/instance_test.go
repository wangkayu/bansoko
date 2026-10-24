package persistence

import (
	"context"
	"strings"
	"testing"
)

func TestLoadInstanceProgramWithoutDatabaseFailsClosed(t *testing.T) {
	_, err := LoadInstanceProgram(context.Background(), nil, "anime")
	if err == nil || !strings.Contains(err.Error(), "database is not wired") {
		t.Fatalf("missing database must return explicit history error: %v", err)
	}
}
