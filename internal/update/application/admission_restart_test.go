package application

import (
	"errors"
	"path/filepath"
	"testing"

	"bansoko/internal/apperrors"
	"bansoko/internal/update/journal"
)

func TestAdmissionWaitsForBootstrapToFinishUpdate(t *testing.T) {
	for _, state := range []string{"pending marker", "active journal"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			switch state {
			case "pending marker":
				marker := PendingMarker{OperationID: "existing-op", ReleaseID: "v1.0.0", ReleaseSequence: 1, ManifestDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
				if err := WritePendingMarker(root, marker); err != nil {
					t.Fatal(err)
				}
			case "active journal":
				store, err := journal.Open(filepath.Join(root, "journal.json"))
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				if _, err := store.Create(journal.Record{OperationID: "existing-op", Channel: Channel, TargetReleaseID: "v1.0.0", TargetReleaseSequence: 1}); err != nil {
					t.Fatal(err)
				}
			}
			s, err := NewService(Deps{
				UpdateMode: "embedded", UpdatesRoot: root,
				CheckBackupTools: func() error { t.Fatal("admission reached backup tool check"); return nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = s.Admit(t.Context(), "", "key")
			var failure *apperrors.Business
			if !errors.As(err, &failure) || failure.Status != 409 || failure.Code != "UPDATE_LOCKED" {
				t.Fatalf("admission error = %v", err)
			}
		})
	}
}
