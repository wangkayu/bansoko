package transport

import (
	"encoding/json"
	"time"

	filemaintenance "bansoko/internal/filemaintenance"
	"bansoko/internal/transport/httpapi/generated"
)

func ReplacementPolicyDefaultInput(value generated.ReplacementPolicyDefaultInput) (filemaintenance.ReplacementPolicyWriteInput, error) {
	policy, err := replacementPolicy(value.AdmissionMode, value.QualityProfile)
	if err != nil {
		return filemaintenance.ReplacementPolicyWriteInput{}, err
	}
	return filemaintenance.ReplacementPolicyWriteInput{ExpectedVersion: int64(value.ExpectedVersion), Policy: policy}, nil
}

func InstanceReplacementPolicyInput(value generated.InstanceReplacementPolicyInput) (filemaintenance.ReplacementPolicyWriteInput, error) {
	policy, err := replacementPolicy(value.AdmissionMode, value.QualityProfile)
	if err != nil {
		return filemaintenance.ReplacementPolicyWriteInput{}, err
	}
	return filemaintenance.ReplacementPolicyWriteInput{ExpectedVersion: int64(value.ExpectedVersion), Policy: policy}, nil
}

func replacementPolicy(mode generated.ReplacementAdmissionMode, profile any) (filemaintenance.ReplacementPolicy, error) {
	profileRaw, err := json.Marshal(profile)
	if err != nil {
		return filemaintenance.ReplacementPolicy{}, err
	}
	return filemaintenance.ReplacementPolicy{
		AdmissionMode:  filemaintenance.AdmissionMode(mode),
		QualityProfile: profileRaw,
	}, nil
}

func ReplacementPolicyDefaultView(value filemaintenance.ReplacementPolicyDefaultView) (generated.ReplacementPolicyDefaultView, error) {
	var view generated.ReplacementPolicyDefaultView
	if err := unmarshalReplacementPolicyView(value.Policy, value.UpdatedAt, value.Version, &view); err != nil {
		return generated.ReplacementPolicyDefaultView{}, err
	}
	return view, nil
}

func InstanceReplacementPolicyView(value filemaintenance.InstanceReplacementPolicyView) (generated.InstanceReplacementPolicyView, error) {
	var view generated.InstanceReplacementPolicyView
	if err := unmarshalReplacementPolicyView(value.Policy, value.UpdatedAt, value.Version, &view); err != nil {
		return generated.InstanceReplacementPolicyView{}, err
	}
	view.ManagedAnimeId = generated.ID(value.ManagedAnimeID)
	view.SourceDefaultVersion = int(value.SourceDefaultVersion)
	return view, nil
}

type replacementPolicyWire struct {
	AdmissionMode  string          `json:"admission_mode"`
	QualityProfile json.RawMessage `json:"quality_profile"`
	UpdatedAt      *time.Time      `json:"updated_at"`
	Version        int             `json:"version"`
}

func unmarshalReplacementPolicyView(policy filemaintenance.ReplacementPolicy, updatedAt *time.Time, version int64, target any) error {
	raw, err := json.Marshal(replacementPolicyWire{
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
