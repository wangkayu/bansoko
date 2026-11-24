package httpapi

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"testing"
)

type thumbCoverService struct {
	body        []byte
	contentType string
}

func (f *thumbCoverService) CoverPaths(_ context.Context, _ string, _ []string) map[string]string {
	return map[string]string{}
}

func (f *thumbCoverService) FetchCover(_ context.Context, _ string) (io.ReadCloser, string, error) {
	return io.NopCloser(bytes.NewReader(f.body)), f.contentType, nil
}

func makeTestJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	var state uint32 = 0x12345678
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			state = state*1664525 + 1013904223
			img.Set(x, y, color.RGBA{
				R: uint8(state >> 24),
				G: uint8(state >> 16),
				B: uint8(state >> 8),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

func makeTestPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			// 4x4 pseudo-random tiles produce realistic ~200-400 KB lossless PNGs
			// well under the 2 MiB proxy bound while remaining rich enough that
			// a 320px JPEG thumbnail is dramatically smaller.
			bx, by := uint32(x/4), uint32(y/4)
			h := (bx*374761393 + by*668265263) ^ 0x5bf03635
			h = (h ^ (h >> 13)) * 1274126177
			img.Set(x, y, color.NRGBA{
				R: uint8(h >> 24),
				G: uint8(h >> 16),
				B: uint8(h >> 8),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func TestReadImageCoverThumbnailDerivesCachesAndRevalidates(t *testing.T) {
	orig := makeTestPNG(t, 800, 1200)
	server := coverServer(&thumbCoverService{body: orig, contentType: "image/png"})

	full := getImage(t, server, "/api/v1/images/cover/"+testCoverKey, "")
	if full.Code != http.StatusOK || !bytes.Equal(full.Body.Bytes(), orig) {
		t.Fatalf("full cover = %d len=%d, want 200 len=%d", full.Code, full.Body.Len(), len(orig))
	}
	if full.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("full content type = %q", full.Header().Get("Content-Type"))
	}

	thumb := getImage(t, server, "/api/v1/images/cover/"+testCoverKey+"?size=thumb", "")
	if thumb.Code != http.StatusOK {
		t.Fatalf("thumb status = %d %s", thumb.Code, thumb.Body.String())
	}
	if thumb.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("thumb content type = %q, want image/jpeg", thumb.Header().Get("Content-Type"))
	}
	if thumb.Body.Len() >= len(orig) {
		t.Fatalf("thumb len %d not smaller than original %d", thumb.Body.Len(), len(orig))
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(thumb.Body.Bytes()))
	if err != nil || format != "jpeg" {
		t.Fatalf("decode thumb config: format=%q err=%v", format, err)
	}
	if cfg.Width != thumbTargetWidth || cfg.Height != 480 {
		t.Fatalf("thumb dimensions = %dx%d, want %dx480", cfg.Width, cfg.Height, thumbTargetWidth)
	}
	if thumb.Header().Get("ETag") == full.Header().Get("ETag") {
		t.Fatal("thumb ETag must differ from full ETag")
	}
	if thumb.Header().Get("Cache-Control") != "private, max-age=86400, immutable" {
		t.Fatalf("thumb Cache-Control = %q", thumb.Header().Get("Cache-Control"))
	}

	// Revalidation with thumb ETag returns 304; full ETag does not match thumb.
	reval := getImage(t, server, "/api/v1/images/cover/"+testCoverKey+"?size=thumb", thumb.Header().Get("ETag"))
	if reval.Code != http.StatusNotModified || reval.Body.Len() != 0 {
		t.Fatalf("thumb revalidation = %d len=%d", reval.Code, reval.Body.Len())
	}
	mismatch := getImage(t, server, "/api/v1/images/cover/"+testCoverKey+"?size=thumb", full.Header().Get("ETag"))
	if mismatch.Code != http.StatusOK || !bytes.Equal(mismatch.Body.Bytes(), thumb.Body.Bytes()) {
		t.Fatalf("thumb with full ETag = %d, want 200 thumb bytes", mismatch.Code)
	}

	// Explicit size=full returns the exact original.
	explicitFull := getImage(t, server, "/api/v1/images/cover/"+testCoverKey+"?size=full", "")
	if explicitFull.Code != http.StatusOK || !bytes.Equal(explicitFull.Body.Bytes(), orig) {
		t.Fatalf("explicit size=full = %d len=%d", explicitFull.Code, explicitFull.Body.Len())
	}

	// Invalid size is rejected with 400.
	invalid := getImage(t, server, "/api/v1/images/cover/"+testCoverKey+"?size=huge", "")
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid size status = %d, want 400", invalid.Code)
	}
}

func TestReadImagePosterThumbnailAndFallbacks(t *testing.T) {
	large := makeTestJPEG(t, 960, 1440)
	posters := &fakePosterService{body: large, contentType: "image/jpeg"}
	server := posterServer(posters)

	thumb := getImage(t, server, "/api/v1/images/poster/42?size=thumb", "")
	if thumb.Code != http.StatusOK || thumb.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("poster thumb = %d %q", thumb.Code, thumb.Header().Get("Content-Type"))
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(thumb.Body.Bytes()))
	if err != nil || cfg.Width != thumbTargetWidth || cfg.Height != 480 {
		t.Fatalf("poster thumb dims = %dx%d err=%v", cfg.Width, cfg.Height, err)
	}
	if thumb.Header().Get("Cache-Control") != "private, max-age=86400" {
		t.Fatalf("poster thumb Cache-Control = %q", thumb.Header().Get("Cache-Control"))
	}

	// Already narrow image (<= 320px) is never upscaled and returns original bytes.
	small := makeTestJPEG(t, 200, 300)
	posters.body = small
	smallThumb := getImage(t, server, "/api/v1/images/poster/42?size=thumb", "")
	if smallThumb.Code != http.StatusOK || !bytes.Equal(smallThumb.Body.Bytes(), small) {
		t.Fatalf("small image should fall back to original: %d len=%d", smallThumb.Code, smallThumb.Body.Len())
	}

	// Unrecognized / non-decodable image (e.g. AVIF bytes) falls back cleanly to original.
	avif := []byte("\x00\x00\x00\x1cftypavif\x00\x00\x00\x00avifmif1")
	posters.body = avif
	posters.contentType = "image/avif"
	fallback := getImage(t, server, "/api/v1/images/poster/42?size=thumb", "")
	if fallback.Code != http.StatusOK || fallback.Header().Get("Content-Type") != "image/avif" || !bytes.Equal(fallback.Body.Bytes(), avif) {
		t.Fatalf("unsupported format fallback = %d %q", fallback.Code, fallback.Header().Get("Content-Type"))
	}

	// Invalid size is rejected with 400.
	invalid := getImage(t, server, "/api/v1/images/poster/42?size=bad", "")
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid poster size status = %d, want 400", invalid.Code)
	}
}

func TestImageThumbnailCacheEvictsLRUWhenOverCapacity(t *testing.T) {
	imgA := makeTestPNG(t, 640, 960)
	imgB := makeTestJPEG(t, 640, 960)
	derivedA, okA := deriveJPEGThumbnail(imgA, thumbTargetWidth, thumbJPEGQuality)
	derivedB, okB := deriveJPEGThumbnail(imgB, thumbTargetWidth, thumbJPEGQuality)
	if !okA || !okB {
		t.Fatalf("expected both test images to derive thumbnails")
	}
	cache := newImageThumbnailCache(max(len(derivedA), len(derivedB)) + thumbCacheEntryOverhead + 16)
	gotA, ctA := cache.deriveOrOriginal(imgA, "image/png")
	if ctA != "image/jpeg" || !bytes.Equal(gotA, derivedA) {
		t.Fatalf("unexpected first derive")
	}
	gotB, ctB := cache.deriveOrOriginal(imgB, "image/jpeg")
	if ctB != "image/jpeg" || !bytes.Equal(gotB, derivedB) {
		t.Fatalf("unexpected second derive")
	}
	if cache.size > cache.maxBytes || len(cache.entries) != 1 {
		t.Fatalf("cache size=%d maxBytes=%d entries=%d, want 1 entry within budget", cache.size, cache.maxBytes, len(cache.entries))
	}
}
