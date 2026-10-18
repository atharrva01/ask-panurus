// Package server is the HTTP API (PRD section 10) and serves the web page.
// Every interface goes through it, so an answer is identical wherever it is
// asked.
package server

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/atharrva01/ask-panurus/internal/answer"
	"github.com/atharrva01/ask-panurus/internal/index"
	"github.com/atharrva01/ask-panurus/internal/llm"
	"github.com/atharrva01/ask-panurus/internal/retrieve"
)

const (
	maxQuestion  = 1000 // characters
	maxBody      = 8 << 10
	askPerMin    = 30
	searchPerMin = 120
)

//go:embed web/index.html
var indexHTML []byte

// Server holds the API's dependencies.
type Server struct {
	Store     *index.Store
	Retriever *retrieve.Retriever
	Pipeline  *answer.Pipeline // nil serves search only
	Log       *slog.Logger

	ask    *limiter
	search *limiter
}

// Handler returns the routes.
func (s *Server) Handler() http.Handler {
	if s.Log == nil {
		s.Log = slog.Default()
	}
	s.ask, s.search = newLimiter(askPerMin), newLimiter(searchPerMin)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.page)
	mux.HandleFunc("POST /v1/ask", s.handleAsk)
	mux.HandleFunc("POST /v1/search", s.handleSearch)
	mux.HandleFunc("GET /v1/status", s.handleStatus)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	return mux
}

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'")
	w.Write(indexHTML)
}

type askRequest struct {
	Question string   `json:"question"`
	Top      int      `json:"top,omitempty"`
	Mode     string   `json:"mode,omitempty"`
	Kinds    []string `json:"kinds,omitempty"`
}

func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	if !s.ask.allow(clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "rate limit: at most 30 questions per minute")
		return
	}
	if s.Pipeline == nil {
		writeError(w, http.StatusServiceUnavailable, "answers are disabled on this server (no LLM configured); /v1/search still works")
		return
	}
	req, ok := decode(w, r)
	if !ok {
		return
	}
	v, err := s.version(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	start := time.Now()
	a, err := s.Pipeline.Ask(r.Context(), req.Question, v)
	if err != nil {
		if errors.Is(err, llm.ErrNoCredentials) {
			writeError(w, http.StatusServiceUnavailable, "the server has no LLM credentials")
			return
		}
		s.Log.Error("ask failed", "err", err)
		writeError(w, http.StatusInternalServerError, "could not answer; try again")
		return
	}
	// Logs carry the request ID and outcome, never the client address.
	s.Log.Info("ask", "request_id", a.RequestID, "refused", a.Refused, "citations", len(a.Citations),
		"confidence", a.Confidence, "ms", time.Since(start).Milliseconds())
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if !s.search.allow(clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "rate limit: at most 120 searches per minute")
		return
	}
	req, ok := decode(w, r)
	if !ok {
		return
	}
	v, err := s.version(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	top := req.Top
	if top <= 0 || top > 50 {
		top = 10
	}
	hits, err := s.Retriever.Search(r.Context(), req.Question, retrieve.Options{
		VersionID: v.ID, TopK: top, Mode: retrieve.Mode(req.Mode), Kinds: req.Kinds,
	})
	if err != nil {
		s.Log.Error("search failed", "err", err)
		writeError(w, http.StatusInternalServerError, "search failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"indexed_sha": v.Commit, "hits": hits})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	v, err := s.version(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	counts := map[string]int{}
	rows, err := s.Store.Pool.Query(r.Context(), `SELECT kind, count(*) FROM chunks WHERE version_id = $1 GROUP BY kind`, v.ID)
	if err == nil {
		for rows.Next() {
			var k string
			var n int
			if rows.Scan(&k, &n) == nil {
				counts[k] = n
			}
		}
		rows.Close()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"index_version": v.ID, "repo": v.Repo, "indexed_sha": v.Commit, "indexed_at": v.Finished,
		"chunks": counts, "answers_enabled": s.Pipeline != nil,
	})
}

// version resolves the live index version on every request, so switching
// versions needs no restart.
func (s *Server) version(ctx context.Context) (answer.VersionInfo, error) {
	id, err := s.Store.LiveVersion(ctx)
	if err != nil {
		return answer.VersionInfo{}, err
	}
	repo, commit, finished, err := s.Store.VersionMeta(ctx, id)
	return answer.VersionInfo{ID: id, Repo: repo, Commit: commit, Finished: finished}, err
}

func decode(w http.ResponseWriter, r *http.Request) (askRequest, bool) {
	var req askRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "body must be JSON like {\"question\": \"...\"}")
		return req, false
	}
	req.Question = strings.TrimSpace(req.Question)
	switch {
	case req.Question == "":
		writeError(w, http.StatusBadRequest, "question is empty")
		return req, false
	case len(req.Question) > maxQuestion:
		writeError(w, http.StatusBadRequest, "question is longer than 1000 characters")
		return req, false
	}
	return req, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// clientIP is the address used for rate limiting only. It is held in memory
// for at most a minute and never logged (PRD section 5, Privacy).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// limiter allows n requests per client per fixed one-minute window.
type limiter struct {
	mu     sync.Mutex
	n      int
	window time.Time
	counts map[string]int
	now    func() time.Time
}

func newLimiter(n int) *limiter {
	return &limiter{n: n, counts: map[string]int{}, now: time.Now}
}

func (l *limiter) allow(client string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if w := l.now().Truncate(time.Minute); !w.Equal(l.window) {
		l.window, l.counts = w, map[string]int{} // forget every address each minute
	}
	if l.counts[client] >= l.n {
		return false
	}
	l.counts[client]++
	return true
}
