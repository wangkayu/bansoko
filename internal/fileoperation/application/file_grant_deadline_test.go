package application

import (
	"testing"
	"time"
)

func TestPublicationGrantDeadlineChargesWholeGateCall(t *testing.T) {
	local := time.Now()
	db := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	deadline, err := publicationGrantDeadline(local, db, db.Add(10*time.Second), local.Add(4*time.Second))
	if err != nil || deadline.Sub(local) != 10*time.Second {
		t.Fatalf("deadline=%v err=%v", deadline, err)
	}
	for _, elapsed := range []time.Duration{10 * time.Second, 11 * time.Second, -time.Second} {
		if _, err := publicationGrantDeadline(local, db, db.Add(10*time.Second), local.Add(elapsed)); err == nil {
			t.Fatalf("accepted elapsed %v", elapsed)
		}
	}
	for _, until := range []time.Time{time.Time{}, db, db.Add(25 * time.Hour)} {
		if _, err := publicationGrantDeadline(local, db, until, local); err == nil {
			t.Fatalf("accepted deadline %v", until)
		}
	}
}
