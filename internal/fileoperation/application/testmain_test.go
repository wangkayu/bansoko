package application

import (
	"os"
	"testing"

	"bansoko/internal/namingvars"
)

func TestMain(m *testing.M) {
	vars := namingvars.Vars{"test": {"marker": "ok"}}
	varsFingerprint, err := namingvars.Fingerprint(vars)
	if err != nil {
		panic(err)
	}
	if err := namingvars.SetActive(vars, varsFingerprint); err != nil {
		panic(err)
	}

	os.Exit(m.Run())
}
