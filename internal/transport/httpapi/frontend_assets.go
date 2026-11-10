package httpapi

import (
	"mime"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
)

var hashedFrontendAsset = regexp.MustCompile(`^/assets/[^/]+-[A-Za-z0-9_-]{8,}\.(js|css|woff2?|ttf|otf|svg|png|jpe?g|webp|avif|ico)$`)

// Vite assets are content addressed. The HTML and service worker are not, so
// neither receives an immutable lifetime. Missing assets must remain uncached.
func serveFrontendAsset(w http.ResponseWriter, r *http.Request, dir string) bool {
	if !hashedFrontendAsset.MatchString(r.URL.Path) {
		return false
	}
	fs := http.Dir(dir)
	f, err := fs.Open(r.URL.Path)
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
				compressed, err := fs.Open(r.URL.Path + suffix)
				if err != nil {
					continue
				}
				stat, err := compressed.Stat()
				if err != nil || stat.IsDir() {
					compressed.Close()
					continue
				}
				w.Header().Set("Content-Encoding", encoding)
				http.ServeContent(w, r, info.Name(), stat.ModTime(), compressed)
				compressed.Close()
				return true
			}
		}
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
	return true
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
