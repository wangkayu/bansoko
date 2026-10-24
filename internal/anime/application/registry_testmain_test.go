package application

import (
	"os"
	"testing"

	"bansoko/internal/testutil/animetest"
)

func TestMain(m *testing.M) {
	animetest.InstallRegistries()
	os.Exit(m.Run())
}
