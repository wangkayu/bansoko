package httpapi

import (
	"bytes"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultFrontendDir is the conventional on-disk vite build output used when
// no explicit directory is configured and the binary carries no embedded
// assets (development builds).
const DefaultFrontendDir = "frontend/dist"

// ResolveFrontendFS selects the web UI asset source:
//  1. an explicitly configured directory (BANSOKO_FRONTEND_DIR/--frontend-dir),
//  2. the assets embedded in the binary (embed_frontend builds),
//  3. the on-disk DefaultFrontendDir when it exists,
//  4. nil, which makes the root handler answer 404.
//
// The embedded tree wins over the default disk path so a release binary
// serves exactly the UI it shipped with regardless of the working directory,
// while explicit operator configuration always overrides both.
func ResolveFrontendFS(explicitDir string, embedded fs.FS) fs.FS {
	if strings.TrimSpace(explicitDir) != "" {
		return os.DirFS(explicitDir)
	}
	if embedded != nil {
		return embedded
	}
	if info, err := os.Stat(DefaultFrontendDir); err == nil && info.IsDir() {
		return os.DirFS(DefaultFrontendDir)
	}
	return nil
}

var hashedFrontendAsset = regexp.MustCompile(`^/assets/[^/]+-[A-Za-z0-9_-]{8,}\.(js|css|woff2?|ttf|otf|svg|png|jpe?g|webp|avif|ico)$`)

// Vite assets are content addressed. The HTML and service worker are not, so
// neither receives an immutable lifetime. Missing assets must remain uncached.
func serveFrontendAsset(w http.ResponseWriter, r *http.Request, assets fs.FS) bool {
	if !hashedFrontendAsset.MatchString(r.URL.Path) {
		return false
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	f, err := assets.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return false
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	ext := path.Ext(r.URL.Path)
	if contentType := mime.TypeByExtension(ext); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if ext == ".js" || ext == ".css" {
		w.Header().Add("Vary", "Accept-Encoding")
		// Range requests retain the original representation and normal byte offsets.
		if r.Header.Get("Range") == "" {
			for _, encoding := range preferredAssetEncodings(r.Header.Get("Accept-Encoding")) {
				suffix := ".gz"
				if encoding == "br" {
					suffix = ".br"
				}
				compressed, err := assets.Open(name + suffix)
				if err != nil {
					continue
				}
				stat, err := compressed.Stat()
				if err != nil || stat.IsDir() {
					compressed.Close()
					continue
				}
				w.Header().Set("Content-Encoding", encoding)
				serveAssetContent(w, r, info.Name(), stat.ModTime(), compressed)
				compressed.Close()
				return true
			}
		}
	}
	serveAssetContent(w, r, info.Name(), info.ModTime(), f)
	return true
}

// serveAssetContent answers with http.ServeContent semantics (range support,
// conditional requests). fs.File does not guarantee io.ReadSeeker, so
// non-seekable sources (not produced by the embed or os.DirFS implementations
// used in production) are buffered first.
func serveAssetContent(w http.ResponseWriter, r *http.Request, name string, modtime time.Time, content fs.File) {
	if seeker, ok := content.(io.ReadSeeker); ok {
		http.ServeContent(w, r, name, modtime, seeker)
		return
	}
	data, err := io.ReadAll(content)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, name, modtime, bytes.NewReader(data))
}

func preferredAssetEncodings(header string) []string {
	qualities := map[string]float64{}
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(part, ";")
		name := strings.ToLower(strings.TrimSpace(fields[0]))
		quality := 1.0
		for _, field := range fields[1:] {
			key, value, ok := strings.Cut(strings.TrimSpace(field), "=")
			if ok && strings.EqualFold(key, "q") {
				parsed, err := strconv.ParseFloat(value, 64)
				if err != nil || !(parsed >= 0 && parsed <= 1) {
					quality = 0
				} else {
					quality = parsed
				}
			}
		}
		qualities[name] = quality
	}
	quality := func(name string) float64 {
		if q, ok := qualities[name]; ok {
			return q
		}
		return qualities["*"]
	}
	encodings := []string{"br", "gzip"}
	if quality("gzip") > quality("br") {
		encodings[0], encodings[1] = encodings[1], encodings[0]
	}
	accepted := encodings[:0]
	for _, encoding := range encodings {
		if quality(encoding) > 0 {
			accepted = append(accepted, encoding)
		}
	}
	return accepted
}
