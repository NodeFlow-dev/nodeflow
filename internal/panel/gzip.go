package panel

import (
	"compress/gzip"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// gzipMinBytes is the smallest body worth compressing; tiny JSON replies
// would only grow with the gzip header.
const gzipMinBytes = 1024

var gzipWriterPool = sync.Pool{New: func() any {
	w, _ := gzip.NewWriterLevel(io.Discard, gzip.DefaultCompression)
	return w
}}

// gzipCompressible reports whether a response Content-Type benefits from
// compression. Binary artifacts, images and already-compressed payloads are
// passed through unchanged.
func gzipCompressible(contentType string) bool {
	if contentType == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	switch {
	case strings.HasPrefix(mediaType, "text/"):
		return true
	case mediaType == "application/json", mediaType == "application/javascript",
		mediaType == "application/manifest+json", mediaType == "image/svg+xml",
		strings.HasSuffix(mediaType, "+json"):
		return true
	}
	return false
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		coding, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(coding), "gzip") {
			continue
		}
		params = strings.ReplaceAll(strings.TrimSpace(params), " ", "")
		if params == "q=0" || params == "q=0.0" || params == "q=0.00" || params == "q=0.000" {
			return false
		}
		return true
	}
	return false
}

// gzipUIResponses applies gzipResponses to the admin API, metrics and the
// embedded web bundle. Agent endpoints (small JSON, signed binary artifacts)
// and /auth/ (session tokens; avoids BREACH-style length oracles) are served
// uncompressed.
func gzipUIResponses(next http.Handler) http.Handler {
	compressed := gzipResponses(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/agent/") || strings.HasPrefix(r.URL.Path, "/auth/") {
			next.ServeHTTP(w, r)
			return
		}
		compressed.ServeHTTP(w, r)
	})
}

// gzipResponses compresses JSON, text and static web responses when the
// client accepts gzip. The decision is deferred until the handler has written
// gzipMinBytes (or finished) so small replies stay uncompressed, and
// Range/partial, HEAD and pre-encoded responses are never touched.
func gzipResponses(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead || r.Header.Get("Range") != "" || !acceptsGzip(r) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Accept-Encoding")
		gw := &gzipResponseWriter{ResponseWriter: w}
		defer gw.finish()
		next.ServeHTTP(gw, r)
	})
}

type gzipResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool // handler called WriteHeader
	decided     bool
	compress    bool
	buf         []byte
	gz          *gzip.Writer
}

func (w *gzipResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = status
	// Informational or bodiless statuses cannot be compressed; pass through.
	if status < 200 || status == http.StatusNoContent || status == http.StatusNotModified ||
		status == http.StatusPartialContent || !w.eligible() {
		w.decide(false)
	}
}

func (w *gzipResponseWriter) eligible() bool {
	h := w.Header()
	if h.Get("Content-Encoding") != "" || h.Get("Content-Range") != "" {
		return false
	}
	if !gzipCompressible(h.Get("Content-Type")) {
		return false
	}
	if raw := h.Get("Content-Length"); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n < gzipMinBytes {
			return false
		}
	}
	return true
}

func (w *gzipResponseWriter) decide(compress bool) {
	if w.decided {
		return
	}
	w.decided = true
	w.compress = compress
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	if compress {
		h := w.Header()
		h.Del("Content-Length")
		h.Set("Content-Encoding", "gzip")
		w.ResponseWriter.WriteHeader(status)
		w.gz = gzipWriterPool.Get().(*gzip.Writer)
		w.gz.Reset(w.ResponseWriter)
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *gzipResponseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		// Implicit 200; the Content-Type may be sniffed by net/http only for
		// uncompressed writes, so require an explicit compressible type.
		w.WriteHeader(http.StatusOK)
	}
	if w.decided {
		if w.compress {
			return w.gz.Write(p)
		}
		return w.ResponseWriter.Write(p)
	}
	w.buf = append(w.buf, p...)
	if len(w.buf) >= gzipMinBytes {
		w.decide(true)
		buffered := w.buf
		w.buf = nil
		if _, err := w.gz.Write(buffered); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Flush forwards to the underlying writer so streaming handlers keep working.
func (w *gzipResponseWriter) Flush() {
	if !w.decided {
		w.decide(len(w.buf) >= gzipMinBytes && w.eligible())
		if len(w.buf) > 0 {
			buffered := w.buf
			w.buf = nil
			if w.compress {
				_, _ = w.gz.Write(buffered)
			} else {
				_, _ = w.ResponseWriter.Write(buffered)
			}
		}
	}
	if w.compress {
		_ = w.gz.Flush()
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *gzipResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *gzipResponseWriter) finish() {
	if !w.decided {
		if !w.wroteHeader && len(w.buf) == 0 {
			// Handler wrote nothing; let net/http send its implicit 200.
			return
		}
		// Short body: send uncompressed with its exact length.
		w.decide(false)
		if len(w.buf) > 0 {
			_, _ = w.ResponseWriter.Write(w.buf)
			w.buf = nil
		}
		return
	}
	if w.compress && w.gz != nil {
		_ = w.gz.Close()
		w.gz.Reset(io.Discard)
		gzipWriterPool.Put(w.gz)
		w.gz = nil
	}
}
