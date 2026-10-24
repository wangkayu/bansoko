package application

import (
	domain "bansoko/internal/anime/domain"
	"bansoko/internal/filemaintenance"
	"bansoko/internal/qualityprofile"
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestBusinessDefaultProfileMatchesFreshMigrationAndTypedContract(t *testing.T) {
	raw, err := os.ReadFile("../../../../db/migrations/001_baseline.sql")
	if err != nil {
		t.Fatal(err)
	}
	// The baseline seed is pretty-printed; compare the compacted JSON.
	seed := regexp.MustCompile(`(?s)VALUES \('retention', 'retention-policy-v1',\s*'(\{.*?\})'::jsonb`).FindSubmatch(raw)
	if seed == nil {
		t.Fatal("retention policy seed not found in the baseline")
	}
	var seeded struct {
		QualityProfile json.RawMessage `json:"quality_profile"`
	}
	if err := json.Unmarshal(seed[1], &seeded); err != nil {
		t.Fatal(err)
	}
	var persisted, code bytes.Buffer
	if err := json.Compact(&persisted, seeded.QualityProfile); err != nil {
	}
	if err := json.Compact(&code, filemaintenance.DefaultQualityProfileJSON); err != nil {
		t.Fatal(err)
	}
	if persisted.String() != code.String() {
		t.Fatal("fresh persisted and code quality defaults differ")
	}
	policy := filemaintenance.DefaultRetentionPolicy()
	if err := filemaintenance.ValidateRetentionPolicy(policy); err != nil {
		t.Fatal(err)
	}
	frozen := frozenBusinessFixture(t)
	frozen.Business.Retention = domain.RetentionValue{AdmissionMode: string(policy.AdmissionMode), QualityProfile: policy.QualityProfile, CleanupPolicy: policy.CleanupPolicy}
	if _, err := frozen.Business.Freeze(); err != nil {
		t.Fatal(err)
	}
	profile, err := qualityprofile.Parse(frozen.Business.Retention.QualityProfile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(profile.Keys(), ",") != "subgroup.score,release.lang,release.resolution,release.revision" {
		t.Fatalf("replacement items=%v", profile.Keys())
	}
}
