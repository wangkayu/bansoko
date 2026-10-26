package transport

import (
	hardeningdomain "bansoko/internal/hardening/domain"
	"bansoko/internal/transport/httpapi/generated"
	updateapplication "bansoko/internal/update/application"
)

// AdmitInput maps the generated request into the application input model.
func AdmitInput(value generated.UpdateAdmitInput) updateapplication.AdmitInput {
	return updateapplication.AdmitInput{
		TargetReleaseID: value.TargetReleaseId,
		ManifestDigest:  value.ManifestDigest,
	}
}

// Status projects an application update status onto the HTTP response shape.
func Status(value updateapplication.Status) generated.EmbeddedUpdateStatus {
	return generated.EmbeddedUpdateStatus{
		OperationId:         value.OperationID,
		Channel:             value.Channel,
		Step:                value.Step,
		TargetReleaseId:     value.TargetReleaseID,
		TargetVersion:       value.TargetVersion,
		TargetSchemaVersion: value.TargetSchemaVersion,
		LastError:           value.LastError,
		CreatedAt:           value.CreatedAt,
		UpdatedAt:           value.UpdatedAt,
	}
}

// Check projects the complete application check result at the HTTP boundary.
func Check(value updateapplication.Check) generated.UpdateCheck {
	result := generated.UpdateCheck{
		CheckedAt: value.CheckedAt,
		Status:    generated.UpdateCheckStatus(value.Status),
		Current: generated.UpdateCurrent{
			ReleaseId:          value.Current.ReleaseID,
			Version:            value.Current.Version,
			ImageCompatibility: value.Current.ImageCompatibility,
		},
		Compatibility: generated.UpdateCompatibility{
			Kind:            generated.UpdateCompatibilityKind(value.Compatibility.Kind),
			MinImageVersion: value.Compatibility.MinImageVersion,
			Reason:          value.Compatibility.Reason,
		},
	}
	if notes := value.Current.ReleaseNotes; notes != nil {
		entry := releaseNotesEntry(*notes)
		result.Current.ReleaseNotes = &entry
	}
	if value.ActiveOperation != nil {
		active := Status(*value.ActiveOperation)
		result.ActiveOperation = &active
	}
	if value.Candidate != nil {
		result.Candidate = &generated.UpdateCandidate{
			ReleaseId:              value.Candidate.ReleaseID,
			Version:                value.Candidate.Version,
			ReleaseSequence:        value.Candidate.ReleaseSequence,
			PublishedAt:            value.Candidate.PublishedAt,
			TargetMigrationVersion: value.Candidate.TargetMigrationVersion,
			RequiredDowntime:       value.Candidate.RequiredDowntime,
			BackupRequired:         value.Candidate.BackupRequired,
			RollbackClass:          value.Candidate.RollbackClass,
			ManifestDigest:         value.Candidate.ManifestDigest,
			PayloadSize:            value.Candidate.PayloadSize,
		}
		if len(value.Candidate.ReleaseNotes) > 0 {
			entries := make([]generated.ReleaseNotesEntry, 0, len(value.Candidate.ReleaseNotes))
			for _, notes := range value.Candidate.ReleaseNotes {
				entries = append(entries, releaseNotesEntry(notes))
			}
			result.Candidate.ReleaseNotes = &entries
		}
	}
	return result
}

func releaseNotesEntry(value hardeningdomain.ReleaseNotesEntry) generated.ReleaseNotesEntry {
	entry := generated.ReleaseNotesEntry{Version: value.Version, PublishedAt: value.PublishedAt}
	entry.Notes.ZhCN = value.Notes.ZhCN
	entry.Notes.En = value.Notes.En
	return entry
}
