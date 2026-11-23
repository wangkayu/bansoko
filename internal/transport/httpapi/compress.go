package httpapi

import (
	"compress/gzip"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// compressMinBytes is the smallest body worth compressing. Smaller JSON fits
// in one packet, so gzip would only add CPU and a header.
const compressMinBytes = 1024

var gzipWriters = sync.Pool{New: func() any {
	w, _ := gzip.NewWriterLevel(nil, 6)
	return w
}}

// compressJSON gzips JSON API responses when the client asks for it. Only
// buffered application/json bodies are compressed: streaming responses (SSE),
// downloads, ranged reads, HEAD and anything that already carries a
// Content-Encoding pass through untouched. The decision is made on the first
// write, from the headers the handler actually produced.
func compressJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead || r.Header.Get("Range") != "" || !acceptsGzip(r.Header.Get("Accept-Encoding")) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Accept-Encoding")
		cw := &compressWriter{ResponseWriter: w}
		defer cw.finish()
		next.ServeHTTP(cw, r)
	})
}

type compressWriter struct {
	http.ResponseWriter
	status  int
	buf     []byte
	decided bool
	gz      *gzip.Writer
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (c *compressWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

func (c *compressWriter) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
	if c.decided {
		return
	}
	// Informational and bodiless statuses never carry a body to compress.
	if status < 200 || status == http.StatusNoContent || status == http.StatusNotModified {
		c.decided = true
		c.ResponseWriter.WriteHeader(status)
	}
}

func (c *compressWriter) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	if c.decided {
		if c.gz != nil {
			return c.gz.Write(p)
		}
		return c.ResponseWriter.Write(p)
	}
	h := c.Header()
	if !compressible(h) {
		c.passthrough()
		return c.ResponseWriter.Write(p)
	}
	c.buf = append(c.buf, p...)
	if len(c.buf) >= compressMinBytes {
		c.startGzip()
	}
	return len(p), nil
}

// Flush forwards to the underlying writer once the response is no longer
// being held back for the compression decision.
func (c *compressWriter) Flush() {
	if !c.decided {
		c.passthrough()
	}
	if c.gz != nil {
		_ = c.gz.Flush()
	}
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (c *compressWriter) passthrough() {
	c.decided = true
	if c.status == 0 {
		c.status = http.StatusOK
	}
	c.ResponseWriter.WriteHeader(c.status)
	if len(c.buf) > 0 {
		_, _ = c.ResponseWriter.Write(c.buf)
		c.buf = nil
	}
}

func (c *compressWriter) startGzip() {
	c.decided = true
	h := c.Header()
	h.Del("Content-Length")
	h.Set("Content-Encoding", "gzip")
	c.ResponseWriter.WriteHeader(c.status)
	gz := gzipWriters.Get().(*gzip.Writer)
	gz.Reset(c.ResponseWriter)
	c.gz = gz
	_, _ = gz.Write(c.buf)
	c.buf = nil
}

func (c *compressWriter) finish() {
	switch {
	case c.gz != nil:
		_ = c.gz.Close()
		gzipWriters.Put(c.gz)
		c.gz = nil
	case !c.decided:
		// Small body (or none): send as is, with an exact length.
		if c.status == 0 {
			c.status = http.StatusOK
		}
		if len(c.buf) > 0 {
			c.Header().Set("Content-Length", strconv.Itoa(len(c.buf)))
		}
		c.ResponseWriter.WriteHeader(c.status)
		if len(c.buf) > 0 {
			_, _ = c.ResponseWriter.Write(c.buf)
		}
	}
}

func compressible(h http.Header) bool {
	if h.Get("Content-Encoding") != "" {
		return false
	}
	ct := strings.ToLower(h.Get("Content-Type"))
	return strings.HasPrefix(ct, "application/json") || strings.Contains(ct, "+json")
}

// acceptsGzip reports whether the header allows gzip (q > 0, or a wildcard).
func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		name := strings.ToLower(strings.TrimSpace(fields[0]))
		if name != "gzip" && name != "*" {
			continue
		}
		q := 1.0
		for _, param := range fields[1:] {
			param = strings.TrimSpace(param)
			if strings.HasPrefix(param, "q=") {
				if v, err := strconv.ParseFloat(param[2:], 64); err == nil {
					q = v
				}
			}
		}
		if q > 0 {
			return true
		}
	}
	return false
}
