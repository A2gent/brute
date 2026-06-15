package http

import (
	"fmt"
	"io"
	stdhttp "net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// EnableHTTPAccessLog turns on concise per-request logs for HTTP-only/server mode.
// TUI startup intentionally does not call this, keeping Bubble Tea output clean.
func (s *Server) EnableHTTPAccessLog(w io.Writer) {
	if s == nil {
		return
	}
	if w == nil {
		w = os.Stdout
	}

	s.httpAccessLogMu.Lock()
	defer s.httpAccessLogMu.Unlock()
	s.httpAccessLogWriter = w
	s.httpAccessLogEnabled = true
}

func (s *Server) setHTTPAccessLogWriter(w io.Writer) {
	if s == nil {
		return
	}
	s.httpAccessLogMu.Lock()
	defer s.httpAccessLogMu.Unlock()
	s.httpAccessLogWriter = w
}

func (s *Server) httpAccessLogSnapshot() (io.Writer, bool) {
	if s == nil {
		return nil, false
	}
	s.httpAccessLogMu.RLock()
	defer s.httpAccessLogMu.RUnlock()
	return s.httpAccessLogWriter, s.httpAccessLogEnabled && s.httpAccessLogWriter != nil
}

func (s *Server) httpAccessLogMiddleware(next stdhttp.Handler) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		writer, enabled := s.httpAccessLogSnapshot()
		if !enabled {
			next.ServeHTTP(w, r)
			return
		}

		requestID := atomic.AddUint64(&s.httpAccessLogSeq, 1)
		started := time.Now()
		path := ""
		if r.URL != nil {
			path = r.URL.Path
		}

		s.writeHTTPAccessLogLine(writer,
			"HTTP request started request_id=%d method=%s path=%s remote=%s user_agent=%q content_length=%d",
			requestID,
			r.Method,
			path,
			r.RemoteAddr,
			r.UserAgent(),
			r.ContentLength,
		)

		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)

		status := ww.Status()
		if status == 0 {
			status = stdhttp.StatusOK
		}
		route := ""
		if routeCtx := chi.RouteContext(r.Context()); routeCtx != nil {
			route = routeCtx.RoutePattern()
		}

		s.writeHTTPAccessLogLine(writer,
			"HTTP request completed request_id=%d method=%s path=%s route=%q status=%d bytes=%d duration=%s",
			requestID,
			r.Method,
			path,
			route,
			status,
			ww.BytesWritten(),
			time.Since(started).Round(time.Millisecond),
		)
	})
}

func (s *Server) writeHTTPAccessLogLine(w io.Writer, format string, args ...interface{}) {
	if s == nil || w == nil {
		return
	}
	line := fmt.Sprintf(format, args...)

	// Serialize writes so concurrent HTTP requests do not interleave in Docker logs.
	s.httpAccessLogMu.Lock()
	defer s.httpAccessLogMu.Unlock()
	_, _ = fmt.Fprintln(w, line)
}
