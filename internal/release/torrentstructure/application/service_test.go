package application

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

type stubLoader struct {
	payload []byte
	err     error
}

func (s stubLoader) LoadPayload(context.Context, string) ([]byte, error) {
	return s.payload, s.err
}

// oneTorrent is a real two-file torrent: a video and a nested subtitle.
const oneTorrent = "d4:infod5:filesld6:lengthi10e4:pathl7:ep1.mkveed6:lengthi20e4:pathl4:Subs7:ep1.asseee4:name7:Show S1ee"

func TestGetReadsTheRetainedPayload(t *testing.T) {
	view, err := NewService(stubLoader{payload: []byte(oneTorrent)}).Get(context.Background(), "release-1")
	if err != nil {
		t.Fatal(err)
	}
	if view.Name != "Show S1" || view.TorrentVersion != "v1" || view.TotalSize != 30 || view.FileCount != 2 {
		t.Fatalf("view = %+v", view)
	}
	if len(view.Files) != 2 || view.Files[0].Path != "Subs/ep1.ass" || view.Files[0].SizeBytes != 20 || view.Files[1].Path != "ep1.mkv" {
		t.Fatalf("files = %+v", view.Files)
	}
	if view.InfoHashV1 == "" || view.Truncated {
		t.Fatalf("view = %+v", view)
	}
}

func TestGetReportsAMissingPayloadAsUnavailable(t *testing.T) {
	_, err := NewService(stubLoader{err: ErrPayloadUnavailable}).Get(context.Background(), "release-1")
	if !errors.Is(err, ErrPayloadUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if _, err := NewService(stubLoader{payload: []byte("not bencode")}).Get(context.Background(), "release-1"); !errors.Is(err, ErrPayloadUnavailable) {
		t.Fatalf("corrupt payload err = %v", err)
	}
}

func TestGetCutsTheFileListAtTheLimit(t *testing.T) {
	var files string
	for index := 0; index < MaxFiles+5; index++ {
		name := fmt.Sprintf("f%04d.txt", index)
		files += fmt.Sprintf("d6:lengthi1e4:pathl%d:%see", len(name), name)
	}
	payload := "d4:infod5:filesl" + files + "e4:name4:Showee"
	view, err := NewService(stubLoader{payload: []byte(payload)}).Get(context.Background(), "release-1")
	if err != nil {
		t.Fatal(err)
	}
	if view.FileCount != MaxFiles+5 || len(view.Files) != MaxFiles || !view.Truncated {
		t.Fatalf("view = %+v files=%d", view, len(view.Files))
	}
}
