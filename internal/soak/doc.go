// Package soak holds the SCALE-034 scale soak: a deterministic synthetic data
// generator and the measurement tests that run against it on the test VM.
//
// Every other file in this package is a _test.go file behind the `soakscale`
// build tag, so default builds, `go test ./...` and scripts/ci/test-soak.sh (which
// only sweeps the `soak` tag) never compile or run any of it. The entry point is
// scripts/verify/soak-scale.sh, which starts a disposable PostgreSQL container on the
// VM and exports the connection variables used here.
package soak
