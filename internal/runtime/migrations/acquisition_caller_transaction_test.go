package migrations

import (
	protocol "bansoko/internal/acquisition/terminal/domain"
	"context"
	"testing"
	"time"
)

func TestAcquisitionCallerCompatibility(t *testing.T) {
	db, _ := setupQualification(t)
	ctx := context.Background()
	for _, none := range []bool{false, true} {
		t.Run(map[bool]string{false: "instance", true: "none"}[none], func(t *testing.T) {
			tag := map[bool]string{false: "caller-instance", true: "caller-none"}[none]
			seedAcceptableRelease(t, db, tag)
			p := validQueued(t, "acq-"+tag, "key-"+tag, tag)
			if none {
				mutateQualification(t, &p, func(q map[string]any) { q["management"] = map[string]any{"kind": "none"} })
			}
			if _, err := callQueuedAccept(t, db, p); err != nil {
				t.Fatal(err)
			}
			var corr, ctype, cid, verdict string
			if err := db.QueryRowContext(ctx, `SELECT dt.correlation_id,dt.causation_type,dt.causation_id,r.verdict FROM download_tasks dt JOIN releases r ON r.id=dt.release_id WHERE dt.id=$1`, p.TaskID).Scan(&corr, &ctype, &cid, &verdict); err != nil {
				t.Fatal(err)
			}
			if corr != "corr-"+p.AcquisitionID || ctype != "release" || cid != p.SourceReleaseID || verdict != "accepted_pending_download" {
				t.Fatalf("compatibility changed: %s %s %s %s", corr, ctype, cid, verdict)
			}
		})
	}
}

func TestAcquisitionCanonicalReceiptTime(t *testing.T) {
	db, _ := setupQualification(t)
	for _, text := range []string{"2026-10-05T01:02:03Z", "2026-10-05T01:02:03.120000Z", "2026-10-05T01:02:03.123450Z", "2026-10-05T01:02:03.123456Z"} {
		t.Run(text, func(t *testing.T) {
			var got string
			if err := db.QueryRowContext(context.Background(), `SELECT rtrim(rtrim(to_char($1::timestamptz AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US'),'0'),'.') || 'Z'`, text).Scan(&got); err != nil {
				t.Fatal(err)
			}
			instant, err := time.Parse(time.RFC3339Nano, text)
			if err != nil {
				t.Fatal(err)
			}
			if got != protocol.FormatTime(instant) {
				t.Fatalf("SQL time %q differs from registry %q", got, protocol.FormatTime(instant))
			}
		})
	}
}
