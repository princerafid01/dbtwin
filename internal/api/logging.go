package api

import (
	"log"
	"net/http"
	"time"
)

// logRequests writes one line per request when it's done, e.g.
//
//	POST /v1/branches 201 14.2s
//
// Only the method, path, status and time: never headers (the token is
// in one) and never the body.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond))
	})
}

// statusRecorder passes everything to the real ResponseWriter
// and remembers the status code, which net/http doesn't let us read back.
// It starts at 200 because that's what Go sends if a handler never calls WriteHeader.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Unwrap lets net/http reach the real ResponseWriter underneath
// (http.MaxBytesReader and http.ResponseController use this).
func (s *statusRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}
