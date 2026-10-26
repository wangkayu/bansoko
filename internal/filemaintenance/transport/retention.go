package transport

import (
	"encoding/json"
	"time"

	filemaintenance "bansoko/internal/filemaintenance"
	"bansoko/internal/transport/httpapi/generated"
)

func RetentionPolicyDefaultInput(value generated.RetentionPolicyDefaultInput) (filemaintenance.RetentionPolicyWriteInput, error) {
	policy, err := retentionPolicy(value.AdmissionMode, value.QualityProfile)
	if err != nil {
		return filemaintenance.RetentionPolicyWriteInput{}, err
	}
	return filemaintenance.RetentionPolicyWriteInput{ExpectedVersion: int64(value.ExpectedVersion), Policy: policy}, nil
}

func InstanceRetentionPolicyInput(value generated.InstanceRetentionPolicyInput) (filemaintenance.RetentionPolicyWriteInput, error) {
	policy, err := retentionPolicy(value.AdmissionMode, value.QualityProfile)
	if err != nil {
		return filemaintenance.RetentionPolicyWriteInput{}, err
	}
	return filemaintenance.RetentionPolicyWriteInput{ExpectedVersion: int64(value.ExpectedVersion), Policy: policy}, nil
}

func retentionPolicy(mode generated.RetentionAdmissionMode, profile any) (filemaintenance.RetentionPolicy, error) {
	profileRaw, err := json.Marshal(profile)
	if err != nil {
		return filemaintenance.RetentionPolicy{}, err
	}
	return filemaintenance.RetentionPolicy{
		AdmissionMode:  filemaintenance.AdmissionMode(mode),
		QualityProfile: profileRaw,
	}, nil
}

func RetentionPolicyDefaultView(value filemaintenance.RetentionPolicyDefaultView) (generated.RetentionPolicyDefaultView, error) {
	var view generated.RetentionPolicyDefaultView
	if err := unmarshalRetentionPolicyView(value.Policy, value.UpdatedAt, value.Version, &view); err != nil {
		return generated.RetentionPolicyDefaultView{}, err
	}
	return view, nil
}

func InstanceRetentionPolicyView(value filemaintenance.InstanceRetentionPolicyView) (generated.InstanceRetentionPolicyView, error) {
	var view generated.InstanceRetentionPolicyView
	if err := unmarshalRetentionPolicyView(value.Policy, value.UpdatedAt, value.Version, &view); err != nil {
		return generated.InstanceRetentionPolicyView{}, err
	}
	view.ManagedAnimeId = generated.ID(value.ManagedAnimeID)
	view.SourceDefaultVersion = int(value.SourceDefaultVersion)
	return view, nil
}

type retentionPolicyWire struct {
	AdmissionMode  string          `json:"admission_mode"`
	QualityProfile json.RawMessage `json:"quality_profile"`
	UpdatedAt      *time.Time      `json:"updated_at"`
	Version        int             `json:"version"`
}

func unmarshalRetentionPolicyView(policy filemaintenance.RetentionPolicy, updatedAt *time.Time, version int64, target any) error {
	raw, err := json.Marshal(retentionPolicyWire{
		AdmissionMode:  string(policy.AdmissionMode),
		QualityProfile: policy.QualityProfile,
		UpdatedAt:      cloneTime(updatedAt),
		Version:        int(version),
	})
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
