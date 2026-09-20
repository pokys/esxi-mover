package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"esxi-mover/internal/esxi"
	"esxi-mover/internal/migration"
)

//go:embed static/*
var assets embed.FS

type session struct {
	mu                   sync.Mutex
	csrf                 string
	expires              time.Time
	address, fingerprint string
	probeTime            time.Time
	host                 *esxi.Client
	report               *migration.Report
	job                  *migration.Job
}
type Server struct {
	mu           sync.Mutex
	op           sync.Mutex
	sessions     map[string]*session
	token        string
	running      bool
	failures     int
	failureReset time.Time
	Options      migration.Options
}

func New(token string, options migration.Options) *Server {
	return &Server{sessions: map[string]*session{}, token: token, Options: options}
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("GET /api/session", s.auth(s.sessionInfo))
	mux.HandleFunc("POST /api/probe", s.auth(s.probe))
	mux.HandleFunc("POST /api/connect", s.auth(s.connect))
	mux.HandleFunc("POST /api/analyze", s.auth(s.analyze))
	mux.HandleFunc("POST /api/start", s.auth(s.start))
	mux.HandleFunc("POST /api/control", s.auth(s.control))
	mux.HandleFunc("POST /api/rollback", s.auth(s.rollback))
	mux.HandleFunc("GET /api/job", s.auth(s.job))
	mux.HandleFunc("GET /api/log", s.auth(s.log))
	sub, _ := fs.Sub(assets, "static")
	mux.Handle("/", http.FileServer(http.FS(sub)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if r.Method != "GET" && r.Method != "HEAD" {
			scheme := "https"
			if r.TLS == nil {
				scheme = "http"
			}
			if origin := r.Header.Get("Origin"); origin != "" && origin != scheme+"://"+r.Host {
				problem(w, 403, "Origin rejected")
				return
			}
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				problem(w, 403, "Cross-site request rejected")
				return
			}
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				problem(w, 415, "JSON request required")
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func problem(w http.ResponseWriter, status int, msg string) {
	respond(w, status, map[string]string{"error": msg})
}
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		problem(w, 400, "Invalid request")
		return false
	}
	if e := d.Decode(&struct{}{}); e != io.EOF {
		problem(w, 400, "Invalid trailing request content")
		return false
	}
	return true
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct{ Token string }
	if !readJSON(w, r, &in) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Now().After(s.failureReset) {
		s.failures = 0
		s.failureReset = time.Now().Add(time.Minute)
	}
	if s.failures >= 5 {
		problem(w, 429, "Too many login attempts; wait one minute")
		return
	}
	if subtle.ConstantTimeCompare([]byte(in.Token), []byte(s.token)) != 1 {
		s.failures++
		problem(w, 401, "Invalid admin token")
		return
	}
	// Expired idle sessions are removed. An active job's session is retained.
	for id, se := range s.sessions {
		se.mu.Lock()
		expired := time.Now().After(se.expires) && (se.job == nil || se.job.Snapshot().Complete)
		se.mu.Unlock()
		if expired {
			delete(s.sessions, id)
		}
	}
	if len(s.sessions) >= 16 {
		problem(w, 429, "Session limit reached; restart the appliance only after all jobs are complete")
		return
	}
	id := migration.NewID()
	se := &session{csrf: migration.NewID(), expires: time.Now().Add(12 * time.Hour)}
	s.sessions[id] = se
	http.SetCookie(w, &http.Cookie{Name: "mover_session", Value: id, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: 8 * 24 * 3600})
	respond(w, 200, map[string]string{"csrf": se.csrf})
}
func (s *Server) auth(next func(http.ResponseWriter, *http.Request, *session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, e := r.Cookie("mover_session")
		if e != nil {
			problem(w, 401, "Sign in with the startup admin token")
			return
		}
		s.mu.Lock()
		se := s.sessions[cookie.Value]
		s.mu.Unlock()
		if se == nil {
			problem(w, 401, "Session expired")
			return
		}
		se.mu.Lock()
		if se.job != nil && !se.job.Snapshot().Complete {
			se.expires = time.Now().Add(12 * time.Hour)
		}
		valid := time.Now().Before(se.expires)
		csrf := se.csrf
		se.mu.Unlock()
		if !valid {
			problem(w, 401, "Session expired")
			return
		}
		if r.Method != "GET" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(csrf)) != 1 {
			problem(w, 403, "CSRF token rejected")
			return
		}
		next(w, r, se)
	}
}
func (s *Server) sessionInfo(w http.ResponseWriter, r *http.Request, se *session) {
	se.mu.Lock()
	defer se.mu.Unlock()
	respond(w, 200, map[string]any{"csrf": se.csrf, "connected": se.host != nil, "hasJob": se.job != nil, "address": se.address})
}
func (s *Server) begin(w http.ResponseWriter) bool {
	if !s.op.TryLock() {
		problem(w, 409, "Another management request is in progress")
		return false
	}
	s.mu.Lock()
	running := s.running
	s.mu.Unlock()
	if running {
		s.op.Unlock()
		problem(w, 409, "An active migration is already running")
		return false
	}
	return true
}
func address(host string, port int) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" || strings.ContainsAny(host, " /\\\r\n\x00") || port < 1 || port > 65535 {
		return "", fmt.Errorf("invalid host or port")
	}
	return net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(port)), nil
}
func (s *Server) probe(w http.ResponseWriter, r *http.Request, se *session) {
	if !s.begin(w) {
		return
	}
	defer s.op.Unlock()
	var in struct {
		Host string
		Port int
	}
	if !readJSON(w, r, &in) {
		return
	}
	addr, e := address(in.Host, in.Port)
	if e != nil {
		problem(w, 400, e.Error())
		return
	}
	fp, e := esxi.ProbeHostKey(r.Context(), addr)
	if e != nil {
		problem(w, 400, e.Error())
		return
	}
	se.mu.Lock()
	se.address = addr
	se.fingerprint = fp
	se.probeTime = time.Now()
	se.mu.Unlock()
	respond(w, 200, map[string]string{"fingerprint": fp, "address": addr})
}
func (s *Server) connect(w http.ResponseWriter, r *http.Request, se *session) {
	if !s.begin(w) {
		return
	}
	defer s.op.Unlock()
	var in struct {
		Username, Password, PrivateKey, Passphrase, Fingerprint string
		Confirmed                                               bool
	}
	if !readJSON(w, r, &in) {
		return
	}
	se.mu.Lock()
	addr, fp, at := se.address, se.fingerprint, se.probeTime
	se.mu.Unlock()
	if !in.Confirmed || fp == "" || time.Since(at) > 10*time.Minute || subtle.ConstantTimeCompare([]byte(fp), []byte(in.Fingerprint)) != 1 {
		problem(w, 400, "Probe and explicitly confirm the displayed host key first")
		return
	}
	executor, e := esxi.NewSSH(esxi.SSHOptions{Address: addr, User: in.Username, Password: in.Password, PrivateKey: in.PrivateKey, Passphrase: in.Passphrase, Fingerprint: fp})
	if e != nil {
		problem(w, 400, e.Error())
		return
	}
	host := esxi.NewClient(executor)
	inventory, e := host.Inventory(r.Context())
	if e != nil {
		problem(w, 400, e.Error())
		return
	}
	se.mu.Lock()
	if se.host != nil {
		if closer, ok := se.host.Exec.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}
	se.host = host
	se.report = nil
	se.job = nil
	se.mu.Unlock()
	respond(w, 200, inventory)
}
func (s *Server) analyze(w http.ResponseWriter, r *http.Request, se *session) {
	if !s.begin(w) {
		return
	}
	defer s.op.Unlock()
	var in migration.Request
	if !readJSON(w, r, &in) {
		return
	}
	se.mu.Lock()
	host := se.host
	se.report = nil
	se.mu.Unlock()
	if host == nil {
		problem(w, 400, "Connect to ESXi first")
		return
	}
	report, e := (migration.Analyzer{Host: host, ApplianceUUID: s.Options.ApplianceUUID}).Analyze(r.Context(), in)
	if e != nil {
		problem(w, 400, e.Error())
		return
	}
	se.mu.Lock()
	se.report = &report
	se.mu.Unlock()
	respond(w, 200, report)
}
func (s *Server) start(w http.ResponseWriter, r *http.Request, se *session) {
	if !s.begin(w) {
		return
	}
	defer s.op.Unlock()
	var in struct {
		AnalysisID           string
		MaintenanceConfirmed bool
	}
	if !readJSON(w, r, &in) {
		return
	}
	se.mu.Lock()
	if !in.MaintenanceConfirmed {
		se.mu.Unlock()
		problem(w, 400, "Confirm exclusive maintenance access and a current backup")
		return
	}
	if se.host == nil || se.report == nil || !se.report.Ready || se.report.ID != in.AnalysisID {
		se.mu.Unlock()
		problem(w, 409, "A matching ready analysis is required")
		return
	}
	job := migration.NewJob(*se.report)
	se.job = job
	se.report = nil
	engine := &migration.Engine{Host: se.host, Options: s.Options}
	se.mu.Unlock()
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	go func() {
		defer func() { s.mu.Lock(); s.running = false; s.mu.Unlock() }()
		engine.Run(context.Background(), job)
	}()
	respond(w, 202, job.Snapshot())
}
func (s *Server) job(w http.ResponseWriter, r *http.Request, se *session) {
	se.mu.Lock()
	job := se.job
	se.mu.Unlock()
	if job == nil {
		problem(w, 404, "No migration job in this session")
		return
	}
	respond(w, 200, job.Snapshot())
}
func (s *Server) log(w http.ResponseWriter, r *http.Request, se *session) {
	se.mu.Lock()
	host := se.host
	se.mu.Unlock()
	if host == nil {
		problem(w, 404, "No ESXi connection")
		return
	}
	respond(w, 200, host.Audit.Events())
}
func (s *Server) control(w http.ResponseWriter, r *http.Request, se *session) {
	var in struct {
		Action         string
		ForceConfirmed bool
	}
	if !readJSON(w, r, &in) {
		return
	}
	se.mu.Lock()
	job := se.job
	se.mu.Unlock()
	if job == nil {
		problem(w, 404, "No migration job")
		return
	}
	if e := job.Control(in.Action, in.ForceConfirmed); e != nil {
		problem(w, 409, e.Error())
		return
	}
	respond(w, 200, map[string]bool{"accepted": true})
}
func (s *Server) rollback(w http.ResponseWriter, r *http.Request, se *session) {
	if !s.begin(w) {
		return
	}
	defer s.op.Unlock()
	var in struct{ Confirmed bool }
	if !readJSON(w, r, &in) {
		return
	}
	if !in.Confirmed {
		problem(w, 400, "Explicit registration rollback confirmation required")
		return
	}
	se.mu.Lock()
	job, host := se.job, se.host
	se.mu.Unlock()
	if job == nil || host == nil {
		problem(w, 404, "No migration job")
		return
	}
	if e := (&migration.Engine{Host: host, Options: s.Options}).Rollback(r.Context(), job); e != nil {
		problem(w, 409, e.Error())
		return
	}
	respond(w, 200, job.Snapshot())
}
