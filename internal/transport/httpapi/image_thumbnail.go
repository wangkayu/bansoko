package httpapi

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"math"
	"sync"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
	"golang.org/x/sync/singleflight"
)

const (
	// thumbTargetWidth is the target display width (px) for card thumbnails
	// (about 2x retina for ~160px poster cards). Originals at or below this
	// width are never upscaled.
	thumbTargetWidth = 320
	// thumbJPEGQuality balances small payload (~20-35 KB) with clean card art.
	thumbJPEGQuality = 82
	// thumbMaxPixels rejects decompression bombs before allocating full buffers.
	thumbMaxPixels = 16_000_000
	// thumbMaxCacheBytes bounds the in-process LRU cache of derived thumbnails.
	thumbMaxCacheBytes = 32 << 20
	// thumbCacheEntryOverhead accounts for map/list metadata so even useOriginal
	// entries (whose payload slice is empty) are bounded and evicted by LRU.
	thumbCacheEntryOverhead = 128
)

type thumbCacheEntry struct {
	key         string
	useOriginal bool
	data        []byte
	contentType string
	elem        *list.Element
}

// imageThumbnailCache derives and caches width-bounded JPEG thumbnails keyed by
// the SHA-256 of the original image bytes. Derived entries are pure functions
// of the original bytes, so a replaced poster naturally gets a new key and
// never collides with stale thumbnails.
type imageThumbnailCache struct {
	maxBytes int

	mu      sync.Mutex
	entries map[string]*thumbCacheEntry
	lru     *list.List
	size    int
	flights singleflight.Group
}

func newImageThumbnailCache(maxBytes int) *imageThumbnailCache {
	if maxBytes <= 0 {
		maxBytes = thumbMaxCacheBytes
	}
	return &imageThumbnailCache{
		maxBytes: maxBytes,
		entries:  make(map[string]*thumbCacheEntry),
		lru:      list.New(),
	}
}

func (c *imageThumbnailCache) deriveOrOriginal(original []byte, origContentType string) ([]byte, string) {
	sum := sha256.Sum256(original)
	key := hex.EncodeToString(sum[:])

	if entry, ok := c.get(key); ok {
		if entry.useOriginal {
			return original, origContentType
		}
		return entry.data, entry.contentType
	}

	value, _, _ := c.flights.Do(key, func() (any, error) {
		if entry, ok := c.get(key); ok {
			return entry, nil
		}
		derived, ok := deriveJPEGThumbnail(original, thumbTargetWidth, thumbJPEGQuality)
		entry := &thumbCacheEntry{key: key}
		if !ok {
			entry.useOriginal = true
		} else {
			entry.data = derived
			entry.contentType = "image/jpeg"
		}
		c.put(entry)
		return entry, nil
	})
	entry := value.(*thumbCacheEntry)
	if entry.useOriginal {
		return original, origContentType
	}
	return entry.data, entry.contentType
}

func (c *imageThumbnailCache) get(key string) (*thumbCacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	c.lru.MoveToBack(entry.elem)
	return entry, true
}

func thumbEntryCost(entry *thumbCacheEntry) int {
	return len(entry.data) + thumbCacheEntryOverhead
}

func (c *imageThumbnailCache) put(entry *thumbCacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.entries[entry.key]; ok {
		c.size -= thumbEntryCost(existing)
		c.lru.Remove(existing.elem)
		delete(c.entries, entry.key)
	}
	cost := thumbEntryCost(entry)
	if cost > c.maxBytes {
		return
	}
	entry.elem = c.lru.PushBack(entry)
	c.entries[entry.key] = entry
	c.size += cost
	for c.size > c.maxBytes {
		oldest := c.lru.Front()
		if oldest == nil {
			break
		}
		victim := oldest.Value.(*thumbCacheEntry)
		c.lru.Remove(oldest)
		delete(c.entries, victim.key)
		c.size -= thumbEntryCost(victim)
	}
}

// deriveJPEGThumbnail decodes JPEG/PNG/GIF/WebP bytes and scales them down to
// targetWidth preserving aspect ratio. Returns (_, false) when the image is
// already narrow enough, unsupported (e.g. AVIF), exceeds pixel bounds, fails
// to decode, or produces a result no smaller than the original.
func deriveJPEGThumbnail(original []byte, targetWidth, quality int) ([]byte, bool) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(original))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, false
	}
	if int64(cfg.Width)*int64(cfg.Height) > thumbMaxPixels {
		return nil, false
	}
	if cfg.Width <= targetWidth {
		return nil, false
	}
	src, _, err := image.Decode(bytes.NewReader(original))
	if err != nil {
		return nil, false
	}
	bounds := src.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()
	if srcW <= targetWidth || srcH <= 0 {
		return nil, false
	}
	targetH := max(1, int(math.Round(float64(srcH)*float64(targetWidth)/float64(srcW))))
	dst := image.NewRGBA(image.Rect(0, 0, targetWidth, targetH))
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	xdraw.BiLinear.Scale(dst, dst.Bounds(), src, bounds, xdraw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: quality}); err != nil {
		return nil, false
	}
	if buf.Len() == 0 || buf.Len() >= len(original) {
		return nil, false
	}
	return buf.Bytes(), true
}
