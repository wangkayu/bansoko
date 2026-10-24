package application

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"bansoko/internal/workitems"
)

// B5 step 5: after the video's handoff the subtitle work actually publishes
// next to the video; a subtitle failure is its own terminal and never undoes
// the completed video.
func TestTypedSubtitleAfterVideo(t *testing.T) {
	for _, failSubtitle := range []bool{false, true} {
		name := "subtitle_lands"
		if failSubtitle {
			name = "subtitle_fails_video_stays_completed"
		}
		t.Run(name, func(t *testing.T) {
			x := newTypedRuntimeFixture(t, nil, true)
			if handled, err := x.runFileWorker(t); !handled || err != nil {
				t.Fatalf("video run: %v %v", handled, err)
			}
			if got := x.state(t, x.opID); got.opState != ownerStateCompleted || x.subtitleWorks(t) != 1 {
				t.Fatalf("video did not complete/hand off: %+v", got)
			}
			if failSubtitle {
				if err := os.Remove(x.subSrc); err != nil {
					t.Fatal(err)
				}
			}
			if handled, err := x.runFileWorker(t); !handled || err != nil {
				t.Fatalf("subtitle run: %v %v", handled, err)
			}
			var subTarget string
			if err := x.db.QueryRow(`SELECT target_path FROM file_operations WHERE id=$1`, x.subtile).Scan(&subTarget); err != nil {
				t.Fatal(err)
			}
			sub := x.state(t, x.subtile)
			if failSubtitle {
				if sub.opState != ownerStateFailed || sub.opCode != "SOURCE_MISSING" || sub.workState != workitems.StateFailed {
					t.Fatalf("subtitle failure: %+v", sub)
				}
				if _, err := os.Lstat(subTarget); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("failed subtitle touched its target: %v", err)
				}
			} else {
				if sub.opState != ownerStateCompleted || sub.workState != workitems.StateSucceeded {
					t.Fatalf("subtitle did not complete: %+v", sub)
				}
				if raw, err := os.ReadFile(subTarget); err != nil || string(raw) != "subtitle" || filepath.Dir(subTarget) != filepath.Dir(x.target) {
					t.Fatalf("subtitle not published next to the video: %q %v %s", raw, err, subTarget)
				}
			}
			if got := x.state(t, x.opID); got.opState != ownerStateCompleted {
				t.Fatalf("video changed by the subtitle outcome: %+v", got)
			}
			if raw, err := os.ReadFile(x.target); err != nil || string(raw) != "source-content" {
				t.Fatalf("video target changed: %q %v", raw, err)
			}
		})
	}
}
