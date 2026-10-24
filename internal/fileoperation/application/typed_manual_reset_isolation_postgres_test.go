package application

import (
	"os"
	"strings"
	"testing"
)

// The behavior test lives in anime/application, which owns the public manual
// retry entry. This static check prevents reintroducing the removed writer.
func TestTypedManualResetCannotRewriteCommittedGraph(t *testing.T) {
	bytes, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bytes), "func (s *Service) ResetForManualRetryTx") || strings.Contains(string(bytes), "func (s *Service) resetForManualRetryTx") {
		t.Fatal("retired reset writer restored")
	}
}
