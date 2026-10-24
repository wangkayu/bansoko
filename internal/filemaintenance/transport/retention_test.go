package transport

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	filemaintenance "bansoko/internal/filemaintenance"
	"bansoko/internal/transport/httpapi/generated"
)

func TestRetentionPolicyInputsMapTheTypedProfile(t *testing.T) {
	profile := generated.QualityProfile{Version: "profile-v1", Items: []generated.QualityCompareItem{
		{Key: "anibt.language", Ranks: [][]string{{"CHS", "CHT"}, {"CHS"}}},
		{Key: "subgroup.score", Ranks: [][]string{}},
	}}
	graceSeconds := 3600
	effect := generated.RetentionPolicyDefaultInputCleanupPolicyEffectQuarantine
	version := "cleanup-v2"
	cleanup := generated.RetentionPolicyDefaultInput_CleanupPolicy{
		Effect:       &effect,
		GraceSeconds: &graceSeconds,
		Version:      &version,
		AdditionalProperties: map[string]interface{}{
			"keep_original": true,
		},
	}
	input, err := RetentionPolicyDefaultInput(generated.RetentionPolicyDefaultInput{
		AdmissionMode:   generated.RetentionAdmissionModePerGroup,
		QualityProfile:  profile,
		CleanupPolicy:   cleanup,
		ExpectedVersion: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if input.ExpectedVersion != 7 || input.Policy.AdmissionMode != filemaintenance.ModePerGroup {
		t.Fatalf("mapped input = %+v", input)
	}
	if got := string(input.Policy.QualityProfile); got != `{"items":[{"key":"anibt.language","ranks":[["CHS","CHT"],["CHS"]]},{"key":"subgroup.score","ranks":[]}],"version":"profile-v1"}` {
		t.Fatalf("quality profile JSON = %s", got)
	}
	var cleanupJSON map[string]interface{}
	if err := json.Unmarshal(input.Policy.CleanupPolicy, &cleanupJSON); err != nil {
		t.Fatal(err)
	}
	if cleanupJSON["keep_original"] != true {
		t.Fatalf("cleanup additional property = %#v", cleanupJSON)
	}
}

func TestRetentionPolicyViewsMapNullableAndTypedFields(t *testing.T) {
	updatedAt := time.Unix(123, 0).UTC()
	policy := filemaintenance.RetentionPolicy{
		AdmissionMode:  filemaintenance.ModeSingleCopy,
		QualityProfile: json.RawMessage(`{"version":"profile-v1","items":[{"key":"release.resolution","ranks":[["1080p"],["720p"]]}]}`),
		CleanupPolicy:  json.RawMessage(`{"version":"cleanup-v3","effect":"purge","grace_seconds":0,"custom_keep":false}`),
	}
	view, err := RetentionPolicyDefaultView(filemaintenance.RetentionPolicyDefaultView{
		Policy:    policy,
		UpdatedAt: &updatedAt,
		Version:   11,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.AdmissionMode != generated.RetentionAdmissionModeSingleCopy || view.Version != 11 || view.UpdatedAt == nil || !view.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("mapped default view = %+v", view)
	}
	want := []generated.QualityCompareItem{{Key: "release.resolution", Ranks: [][]string{{"1080p"}, {"720p"}}}}
	if view.QualityProfile.Version != "profile-v1" || !reflect.DeepEqual(view.QualityProfile.Items, want) {
		t.Fatalf("quality profile = %+v", view.QualityProfile)
	}
	if view.CleanupPolicy.Effect == nil || *view.CleanupPolicy.Effect != generated.RetentionPolicyDefaultViewCleanupPolicyEffectPurge || view.CleanupPolicy.AdditionalProperties["custom_keep"] != false {
		t.Fatalf("cleanup policy = %+v", view.CleanupPolicy)
	}

	instance, err := InstanceRetentionPolicyView(filemaintenance.InstanceRetentionPolicyView{
		ManagedAnimeID:       "anime-1",
		Policy:               policy,
		SourceDefaultVersion: 9,
		UpdatedAt:            nil,
		Version:              12,
	})
	if err != nil {
		t.Fatal(err)
	}
	if instance.ManagedAnimeId != generated.ID("anime-1") || instance.SourceDefaultVersion != 9 || instance.UpdatedAt != nil || instance.Version != 12 {
		t.Fatalf("mapped instance view = %+v", instance)
	}
}
