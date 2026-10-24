package application

import (
	"database/sql"
	"testing"
)

func assertTypedFailedFileResult(t *testing.T, db *sql.DB, artifact, fileRef, reason string) {
	t.Helper()
	var got string
	var count int
	if err := db.QueryRow(`SELECT r.delivery_reason_code FROM acquisition_files r JOIN artifacts a ON a.acquisition_id=r.acquisition_id WHERE a.id=$1 AND r.file_ref=$2 AND r.outcome='failed' AND r.effect_class='no_external_effect'`, artifact, fileRef).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != reason {
		t.Fatalf("failure=%s want=%s", got, reason)
	}
	if err := db.QueryRow(`SELECT count(*) FROM file_operations WHERE artifact_id=$1 AND file_ref=$2`, artifact, fileRef).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed declaration fabricated Operation")
	}
}
