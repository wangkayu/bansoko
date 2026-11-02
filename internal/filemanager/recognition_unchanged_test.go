package filemanager

import (
	"context"
	"testing"
)

// A video whose rendered name is already its own is not moved, but it still
// supplies the name its subtitles follow; the subtitles may render a new name.
func TestRecognitionUnchangedVideoStillNamesItsSubtitles(t *testing.T) {
	f := newRecognitionFixture(t, fakeRecognitionPolicy{})
	ctx := context.Background()
	writeFile(t, f.abs("Loose/1.mkv"), 4)
	writeFile(t, f.abs("Loose/1.zh.ass"), 4)

	preview, err := f.service.PreviewRecognition(ctx, recognitionRequest(f.abs("Loose/1.mkv"), f.abs("Loose/1.zh.ass")))
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	video := recognitionRow(t, preview, "1.mkv")
	if video.Disposition != RecognitionUnchanged || video.TargetName != "1.mkv" {
		t.Fatalf("video row = %+v", video)
	}
	subtitle := recognitionRow(t, preview, "1.zh.ass")
	if subtitle.Disposition != RecognitionRename || subtitle.TargetName != "1.sub.ass" {
		t.Fatalf("subtitle row = %+v", subtitle)
	}
}
