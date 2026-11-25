package httpapi

import (
	"testing"

	"bansoko/frontend"
)

// TestEmbeddedFrontendResolution covers the native build: with the
// embed_frontend tag the compiled-in asset set resolves and serves the SPA
// entry without any on-disk directory.
func TestEmbeddedFrontendResolution(t *testing.T) {
	assets := frontend.FS()
	if !frontend.Embedded() {
		if assets != nil {
			t.Fatal("non-embedded build reported non-nil FS()")
		}
		return
	}
	if assets == nil {
		t.Fatal("embedded build reported but FS() is nil")
	}
	entry, err := assets.Open("index.html")
	if err != nil {
		t.Fatalf("embedded index.html: %v", err)
	}
	_ = entry.Close()
	if got := ResolveFrontendFS("", assets); got == nil {
		t.Fatal("embedded assets were not selected")
	}
}
