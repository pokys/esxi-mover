package web

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"esxi-mover/internal/esxi"
	"esxi-mover/internal/migration"
	"golang.org/x/crypto/ssh"
)

func request(h http.Handler, method, path, body, csrf string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://mover.test"+path, strings.NewReader(body))
	r.TLS = &tls.ConnectionState{}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://mover.test")
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func login(t *testing.T, h http.Handler) (*http.Cookie, string) {
	t.Helper()
	w := request(h, "POST", "/api/login", `{"Token":"test-admin-token"}`, "", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var data map[string]string
	if e := json.Unmarshal(w.Body.Bytes(), &data); e != nil {
		t.Fatal(e)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing cookie")
	}
	return cookies[0], data["csrf"]
}
func TestAuthenticationCookieAndCSRF(t *testing.T) {
	s := New("test-admin-token", migration.DefaultOptions())
	h := s.Handler()
	if w := request(h, "GET", "/api/session", "", "", nil); w.Code != 401 {
		t.Fatal("unauthenticated access")
	}
	cookie, csrf := login(t, h)
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || len(cookie.Value) != 32 || len(csrf) != 32 {
		t.Fatal("unsafe session cookie")
	}
	w := request(h, "POST", "/api/start", `{"AnalysisID":"x","MaintenanceConfirmed":true}`, "wrong", cookie)
	if w.Code != 403 {
		t.Fatal("CSRF bypass")
	}
	w = request(h, "GET", "/api/session", "", "", cookie)
	if w.Code != 200 || strings.Contains(w.Body.String(), "test-admin-token") {
		t.Fatal("session leak")
	}
}
func TestOriginAndContentType(t *testing.T) {
	h := New("test-admin-token", migration.DefaultOptions()).Handler()
	for _, origin := range []string{"https://evil.test", "http://mover.test"} {
		r := httptest.NewRequest("POST", "https://mover.test/api/login", strings.NewReader(`{"Token":"test-admin-token"}`))
		r.TLS = &tls.ConnectionState{}
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("origin accepted", origin)
		}
	}
	r := httptest.NewRequest("POST", "https://mover.test/api/login", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 415 {
		t.Fatal("non-JSON accepted")
	}
}
func TestHostKeyConfirmationAndStartGuards(t *testing.T) {
	s := New("test-admin-token", migration.DefaultOptions())
	h := s.Handler()
	cookie, csrf := login(t, h)
	secret := "password-never-returned"
	body := `{"Username":"root","Password":"` + secret + `","Fingerprint":"invented","Confirmed":true}`
	w := request(h, "POST", "/api/connect", body, csrf, cookie)
	if w.Code != 400 || strings.Contains(w.Body.String(), secret) {
		t.Fatal("host-key confirmation bypass or credentials leaked")
	}
	w = request(h, "POST", "/api/start", `{"AnalysisID":"x","MaintenanceConfirmed":true}`, csrf, cookie)
	if w.Code != 409 {
		t.Fatal("start without Analyze accepted")
	}
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	w = request(h, "POST", "/api/analyze", `{}`, csrf, cookie)
	if w.Code != 409 {
		t.Fatal("concurrent migration accepted")
	}
}
func TestLoginRateLimitAndMalformedJSON(t *testing.T) {
	h := New("test-admin-token", migration.DefaultOptions()).Handler()
	for i := 0; i < 5; i++ {
		if w := request(h, "POST", "/api/login", `{"Token":"wrong"}`, "", nil); w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	if w := request(h, "POST", "/api/login", `{"Token":"test-admin-token"}`, "", nil); w.Code != 429 {
		t.Fatal("no rate limit")
	}
	h = New("test-admin-token", migration.DefaultOptions()).Handler()
	for _, body := range []string{`{"Token":"test-admin-token","extra":1}`, `{"Token":"test-admin-token"} {}`, strings.Repeat("x", 129<<10)} {
		if w := request(h, "POST", "/api/login", body, "", nil); w.Code != 400 {
			t.Fatal("malformed body accepted")
		}
	}
}
func TestStaticSecurityAndNoInlineCode(t *testing.T) {
	h := New("test-admin-token", migration.DefaultOptions()).Handler()
	for _, file := range []string{"/", "/app.js", "/style.css"} {
		w := request(h, "GET", file, "", "", nil)
		if w.Code != 200 || w.Header().Get("Content-Security-Policy") == "" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("static asset security", file, w.Code)
		}
	}
	b, e := assets.ReadFile("static/app.js")
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(b, []byte("innerHTML")) || bytes.Contains(b, []byte("localStorage")) {
		t.Fatal("unsafe UI rendering/storage")
	}
}
func TestEphemeralTLS(t *testing.T) {
	a, fp, e := EphemeralTLS()
	if e != nil || len(a.Certificate) != 1 || len(fp) != 64 {
		t.Fatal(e)
	}
	_, fp2, e := EphemeralTLS()
	if e != nil || fp == fp2 {
		t.Fatal("certificate reused")
	}
}
func TestConcurrentSessionReads(t *testing.T) {
	h := New("test-admin-token", migration.DefaultOptions()).Handler()
	cookie, _ := login(t, h)
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := request(h, "GET", "/api/session", "", "", cookie)
			if w.Code != 200 {
				t.Error(w.Code)
			}
		}()
	}
	wg.Wait()
}

type closingExecutor struct{ closed atomic.Int32 }

func (e *closingExecutor) Run(context.Context, esxi.Command) (esxi.Result, error) {
	return esxi.Result{}, nil
}
func (e *closingExecutor) Close() error { e.closed.Add(1); return nil }

func TestExpiredSessionsCloseOnlyIdleConnections(t *testing.T) {
	for _, trigger := range []string{"login", "request"} {
		t.Run(trigger, func(t *testing.T) {
			s := New("test-admin-token", migration.DefaultOptions())
			idle, active := &closingExecutor{}, &closingExecutor{}
			s.sessions["idle"] = &session{expires: time.Now().Add(-time.Hour), host: esxi.NewClient(idle)}
			s.sessions["active"] = &session{expires: time.Now().Add(-time.Hour), host: esxi.NewClient(active), job: migration.NewJob(migration.Report{})}
			h := s.Handler()
			if trigger == "request" {
				w := request(h, "GET", "/api/session", "", "", &http.Cookie{Name: "mover_session", Value: "idle"})
				if w.Code != 401 || idle.closed.Load() != 1 {
					t.Fatal("expired request retained its connection")
				}
			}
			login(t, h)
			if idle.closed.Load() != 1 || active.closed.Load() != 0 || s.sessions["active"] == nil || s.sessions["idle"] != nil {
				t.Fatal("expiry failed to close exactly the idle connection")
			}
		})
	}
}

func TestFailedInventoryClosesSSHConnection(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	closed := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		accepted <- conn
		defer conn.Close()
		defer close(closed)
		server, channels, requests, err := ssh.NewServerConn(conn, cfg)
		if err != nil {
			return
		}
		defer server.Close()
		go ssh.DiscardRequests(requests)
		for channel := range channels {
			ch, reqs, err := channel.Accept()
			if err != nil {
				return
			}
			for req := range reqs {
				_ = req.Reply(true, nil)
				_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{1}))
				ch.Close()
				break
			}
		}
	}()
	s := New("test-admin-token", migration.DefaultOptions())
	h := s.Handler()
	cookie, csrf := login(t, h)
	fp := ssh.FingerprintSHA256(signer.PublicKey())
	se := s.sessions[cookie.Value]
	se.address, se.fingerprint, se.probeTime = ln.Addr().String(), fp, time.Now()
	body, _ := json.Marshal(map[string]any{"Username": "fixture", "Password": "fixture", "Fingerprint": fp, "Confirmed": true})
	w := request(h, "POST", "/api/connect", string(body), csrf, cookie)
	select {
	case conn := <-accepted:
		defer conn.Close()
	case <-time.After(time.Second):
		t.Fatal("SSH connection was not attempted")
	}
	if w.Code != 400 || se.host != nil {
		t.Fatal("failed inventory was accepted")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("failed inventory left SSH connected")
	}
}
