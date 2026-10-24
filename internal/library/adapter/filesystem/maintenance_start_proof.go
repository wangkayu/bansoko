package filesystem

import (
	"bansoko/internal/fileidentity"
	maintenance "bansoko/internal/filemaintenance/executor"
	"bansoko/internal/library/domain"
	"encoding/json"
	"errors"
	"path/filepath"
)

// Source object and both parent identities are captured before mutation. A
// cross-device copy/unlink move cannot satisfy this rename identity proof.
type maintenanceStartProof struct {
	maintenance.MaintenanceInvocation
	SourceObservation domain.FileObservation `json:"source_observation"`
	SourceParent      domain.FileIdentity    `json:"source_parent"`
	TargetParent      domain.FileIdentity    `json:"target_parent"`
}

func captureMaintenanceStart(i maintenance.MaintenanceInvocation) ([]byte, error) {
	source, err := observeFileAs(i.Source, i.Fingerprint)
	if err != nil {
		return nil, err
	}
	if !fileObservationMatches(source, i.Fingerprint, -1) {
		return nil, errors.New("start source fingerprint drift")
	}
	sp, err := fileidentity.FromPath(filepath.Dir(i.Source))
	if err != nil {
		return nil, err
	}
	tp, err := fileidentity.FromPath(filepath.Dir(i.Target))
	if err != nil {
		return nil, err
	}
	return json.Marshal(maintenanceStartProof{i, source, sp, tp})
}
func validateMaintenanceStartAnchors(p maintenanceStartProof) error {
	sp, err := fileidentity.FromPath(filepath.Dir(p.Source))
	if err != nil {
		return err
	}
	tp, err := fileidentity.FromPath(filepath.Dir(p.Target))
	if err != nil {
		return err
	}
	if !fileidentity.Matches(sp, p.SourceParent) || !fileidentity.Matches(tp, p.TargetParent) {
		return errors.New("maintenance frozen anchors changed")
	}
	return nil
}
