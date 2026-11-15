package application

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fileNotificationAdmission struct {
	trigger string
	state   string
	payload map[string]any
}

func fileNotificationAdmissions(t *testing.T, db *sql.DB, operationID string) []fileNotificationAdmission {
	t.Helper()
	rows, err := db.Query(`SELECT trigger_kind,state,payload FROM notification_admissions
WHERE source_owner_type='file_operation' AND source_owner_id=$1 ORDER BY trigger_kind`, operationID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var admissions []fileNotificationAdmission
	for rows.Next() {
		var admission fileNotificationAdmission
		var payload []byte
		if err := rows.Scan(&admission.trigger, &admission.state, &payload); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(payload, &admission.payload); err != nil {
			t.Fatal(err)
		}
		admissions = append(admissions, admission)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return admissions
}

func singleFileNotificationAdmission(t *testing.T, db *sql.DB, operationID, trigger string) fileNotificationAdmission {
	t.Helper()
	for _, admission := range fileNotificationAdmissions(t, db, operationID) {
		if admission.trigger == trigger {
			return admission
		}
	}
	t.Fatalf("no %s admission for operation %s", trigger, operationID)
	return fileNotificationAdmission{}
}

func subtitleList(t *testing.T, admission fileNotificationAdmission) []string {
	t.Helper()
	raw, ok := admission.payload["out.subtitles"]
	if !ok {
		t.Fatalf("admission payload has no out.subtitles: %v", admission.payload)
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("out.subtitles is not a list: %#v", raw)
	}
	paths := make([]string, 0, len(list))
	for _, item := range list {
		path, ok := item.(string)
		if !ok {
			t.Fatalf("out.subtitles item is not a string: %#v", item)
		}
		paths = append(paths, path)
	}
	return paths
}

func assertNoPublishedRoot(t *testing.T, admission fileNotificationAdmission, root string) {
	t.Helper()
	encoded, err := json.Marshal(admission.payload)
	if err != nil {
		t.Fatal(err)
	}
	if root != "" && strings.Contains(string(encoded), root) {
		t.Fatalf("%s admission leaks the published root %q: %s", admission.trigger, root, encoded)
	}
	for _, key := range []string{"out.path", "out.dir"} {
		value, _ := admission.payload[key].(string)
		if filepath.IsAbs(value) {
			t.Fatalf("%s admission %s = %q must be relative", admission.trigger, key, value)
		}
	}
	for _, path := range subtitleList(t, admission) {
		if filepath.IsAbs(path) {
			t.Fatalf("%s admission subtitle %q must be relative", admission.trigger, path)
		}
	}
}

// A bound subtitle's success joins the main video's notification: the video
// admission waits for every bound operation to settle, the subtitle itself is
// resolved without a delivery, and the released payload carries the final
// subtitle paths relative to the frozen notification root.
func TestSubtitleGroupCoalescesSuccessNotification(t *testing.T) {
	x := newTypedRuntimeFixture(t, nil, true)
	if handled, err := x.runFileWorker(t); !handled || err != nil {
		t.Fatalf("video run: %v %v", handled, err)
	}
	video := singleFileNotificationAdmission(t, x.db, x.opID, "file_operation_completed")
	if video.state != "pending_subtitle_completion" {
		t.Fatalf("video admitted before its subtitle settled: %+v", video)
	}
	if paths := subtitleList(t, video); len(paths) != 0 {
		t.Fatalf("video announced subtitle paths before they existed: %v", paths)
	}
	if pending := fileNotificationAdmissions(t, x.db, x.subtile); len(pending) != 0 {
		t.Fatalf("subtitle admitted before it ran: %+v", pending)
	}

	if handled, err := x.runFileWorker(t); !handled || err != nil {
		t.Fatalf("subtitle run: %v %v", handled, err)
	}
	subtitle := singleFileNotificationAdmission(t, x.db, x.subtile, "file_operation_completed")
	if subtitle.state != "resolved" {
		t.Fatalf("bound subtitle must not produce a delivery: %+v", subtitle)
	}
	if got := fileNotificationAdmissions(t, x.db, x.subtile); len(got) != 1 {
		t.Fatalf("subtitle admissions = %+v", got)
	}

	released := singleFileNotificationAdmission(t, x.db, x.opID, "file_operation_completed")
	if released.state != "pending_target_resolution" {
		t.Fatalf("video admission was not released: %+v", released)
	}
	var notificationRoot, subtitleTarget string
	if err := x.db.QueryRow(`SELECT frozen_render_snapshot->>'notification_root' FROM file_operations WHERE id=$1`, x.opID).Scan(&notificationRoot); err != nil {
		t.Fatal(err)
	}
	if err := x.db.QueryRow(`SELECT target_path FROM file_operations WHERE id=$1`, x.subtile).Scan(&subtitleTarget); err != nil {
		t.Fatal(err)
	}
	if notificationRoot == "" {
		t.Fatal("the fixture's frozen render snapshot carries no notification root")
	}
	wantRelative := filepath.ToSlash(strings.TrimPrefix(subtitleTarget, filepath.Clean(notificationRoot)+string(filepath.Separator)))
	paths := subtitleList(t, released)
	if len(paths) != 1 || paths[0] != wantRelative || !strings.HasSuffix(paths[0], ".ass") {
		t.Fatalf("released subtitle paths = %v, want [%q]", paths, wantRelative)
	}
	assertNoPublishedRoot(t, released, notificationRoot)
	assertNoPublishedRoot(t, subtitle, notificationRoot)
}

// A failed subtitle still releases the video admission, with no subtitle path
// claimed, and keeps its own failure notification.
func TestSubtitleGroupReleasesOnSubtitleFailure(t *testing.T) {
	x := newTypedRuntimeFixture(t, nil, true)
	if handled, err := x.runFileWorker(t); !handled || err != nil {
		t.Fatalf("video run: %v %v", handled, err)
	}
	if err := os.Remove(x.subSrc); err != nil {
		t.Fatal(err)
	}
	if handled, err := x.runFileWorker(t); !handled || err != nil {
		t.Fatalf("subtitle run: %v %v", handled, err)
	}
	failure := singleFileNotificationAdmission(t, x.db, x.subtile, "file_operation_failed")
	if failure.state != "pending_target_resolution" {
		t.Fatalf("subtitle failure admission = %+v", failure)
	}
	released := singleFileNotificationAdmission(t, x.db, x.opID, "file_operation_completed")
	if released.state != "pending_target_resolution" {
		t.Fatalf("video admission stayed blocked by a failed subtitle: %+v", released)
	}
	if paths := subtitleList(t, released); len(paths) != 0 {
		t.Fatalf("failed subtitle listed in the video notification: %v", paths)
	}
	var notificationRoot string
	if err := x.db.QueryRow(`SELECT frozen_render_snapshot->>'notification_root' FROM file_operations WHERE id=$1`, x.opID).Scan(&notificationRoot); err != nil {
		t.Fatal(err)
	}
	assertNoPublishedRoot(t, released, notificationRoot)
}
