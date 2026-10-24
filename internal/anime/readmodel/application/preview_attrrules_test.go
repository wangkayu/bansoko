package application

import (
	"bansoko/internal/namingvars"
	"context"
	"strings"
	"testing"
)

func TestExactPreviewAttrRulesMissingReferenceDoesNotConsumeActive(t *testing.T) {
	_, err := (&Service{}).loadPreviewAttrRules(context.Background(), namingvars.ProgramRef{})
	if err == nil || !strings.Contains(err.Error(), "database unavailable") {
		t.Fatalf("missing exact authority must reject: %v", err)
	}
}
