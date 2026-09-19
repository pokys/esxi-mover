package web

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"esxi-mover/internal/migration"
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
