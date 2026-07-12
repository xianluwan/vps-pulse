package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const sessionLifetime = 24 * time.Hour

type loginState struct {
	Failures     int
	WindowStart  time.Time
	BlockedUntil time.Time
}

var loginLimiter = struct {
	sync.Mutex
	Items map[string]loginState
}{Items: map[string]loginState{}}

func (a *App) ensureSecuritySchema() error {
	_, e := a.db.Exec(`CREATE TABLE IF NOT EXISTS sessions(token_hash TEXT PRIMARY KEY,created_at DATETIME NOT NULL,expires_at DATETIME NOT NULL,last_seen DATETIME NOT NULL,client_ip TEXT DEFAULT '',user_agent TEXT DEFAULT '');CREATE INDEX IF NOT EXISTS idx_sessions_expiry ON sessions(expires_at);DELETE FROM sessions WHERE expires_at<=CURRENT_TIMESTAMP`)
	return e
}
func (a *App) migrateSensitiveData() error {
	rows, e := a.db.Query(`SELECT id,token,change_cmd,cf_token FROM vps`)
	if e != nil {
		return e
	}
	type item struct{ id, token, command, cf string }
	items := []item{}
	for rows.Next() {
		var x item
		if e = rows.Scan(&x.id, &x.token, &x.command, &x.cf); e != nil {
			rows.Close()
			return e
		}
		items = append(items, x)
	}
	rows.Close()
	for _, x := range items {
		token := x.token
		if token != "" && !strings.HasPrefix(token, "sha256:") {
			token = agentTokenHash(token)
		}
		command := x.command
		if command != "" && !strings.HasPrefix(command, "enc:v1:") {
			command, e = protectStoredSecret(command)
			if e != nil {
				return e
			}
		}
		cf := x.cf
		if cf != "" && !strings.HasPrefix(cf, "enc:v1:") {
			cf, e = protectStoredSecret(cf)
			if e != nil {
				return e
			}
		}
		if _, e = a.db.Exec(`UPDATE vps SET token=?,change_cmd=?,cf_token=? WHERE id=?`, token, command, cf, x.id); e != nil {
			return e
		}
	}
	return nil
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", 405)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && !a.checkOrigin(r) {
		http.Error(w, "forbidden origin", 403)
		return
	}
	ip := clientIP(r)
	if wait := loginBlocked(ip); wait > 0 {
		w.Header().Set("Retry-After", fmt.Sprint(int(wait.Seconds())+1))
		http.Error(w, "登录尝试过多，请稍后重试", 429)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var body struct {
		Password string `json:"password"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		http.Error(w, "参数错误", 400)
		return
	}
	expected := sha256.Sum256([]byte(a.password))
	actual := sha256.Sum256([]byte(body.Password))
	if subtle.ConstantTimeCompare(expected[:], actual[:]) != 1 {
		recordLoginFailure(ip)
		time.Sleep(250 * time.Millisecond)
		http.Error(w, "密码错误", 401)
		return
	}
	clearLoginFailures(ip)
	raw := make([]byte, 32)
	if _, e := rand.Read(raw); e != nil {
		http.Error(w, "session error", 500)
		return
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := sessionHash(token)
	now := time.Now()
	expires := now.Add(sessionLifetime)
	if _, e := a.db.Exec(`INSERT INTO sessions(token_hash,created_at,expires_at,last_seen,client_ip,user_agent) VALUES(?,?,?,?,?,?)`, hash, now, expires, now, ip, truncateText(r.UserAgent(), 300)); e != nil {
		http.Error(w, "session error", 500)
		return
	}
	secure := strings.HasPrefix(env("PUBLIC_URL", ""), "https://")
	http.SetCookie(w, &http.Cookie{Name: "session", Value: token, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: int(sessionLifetime.Seconds())})
	write(w, map[string]bool{"ok": true})
}
func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if c, e := r.Cookie("session"); e == nil {
		_, _ = a.db.Exec(`DELETE FROM sessions WHERE token_hash=?`, sessionHash(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: "session", Value: "", Path: "/", HttpOnly: true, Secure: strings.HasPrefix(env("PUBLIC_URL", ""), "https://"), SameSite: http.SameSiteStrictMode, MaxAge: -1})
	w.WriteHeader(204)
}
func (a *App) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, e := r.Cookie("session")
		if e != nil || len(c.Value) < 32 {
			http.Error(w, "unauthorized", 401)
			return
		}
		var expires time.Time
		e = a.db.QueryRow(`SELECT expires_at FROM sessions WHERE token_hash=?`, sessionHash(c.Value)).Scan(&expires)
		if e != nil || time.Now().After(expires) {
			http.Error(w, "unauthorized", 401)
			return
		}
		if !a.validRequestOrigin(r) {
			http.Error(w, "forbidden origin", 403)
			return
		}
		_, _ = a.db.Exec(`UPDATE sessions SET last_seen=? WHERE token_hash=?`, time.Now(), sessionHash(c.Value))
		next(w, r)
	}
}
func (a *App) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	return sameOrigin(origin, env("PUBLIC_URL", ""))
}
func (a *App) validRequestOrigin(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return true
	}
	origin := r.Header.Get("Origin")
	return origin == "" || sameOrigin(origin, env("PUBLIC_URL", ""))
}
func sameOrigin(origin, publicURL string) bool {
	o, e1 := url.Parse(origin)
	p, e2 := url.Parse(publicURL)
	return e1 == nil && e2 == nil && strings.EqualFold(o.Scheme, p.Scheme) && strings.EqualFold(o.Host, p.Host)
}
func sessionHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
func agentTokenHash(token string) string {
	sum := sha256.Sum256([]byte("vps-pulse-agent:" + token))
	return "sha256:" + base64.RawURLEncoding.EncodeToString(sum[:])
}
func protectStoredSecret(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	encrypted, e := encryptSecret(value)
	if e != nil {
		return "", e
	}
	return "enc:v1:" + encrypted, nil
}
func revealStoredSecret(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if !strings.HasPrefix(value, "enc:v1:") {
		return value, nil
	}
	return decryptSecret(strings.TrimPrefix(value, "enc:v1:"))
}
func clientIP(r *http.Request) string {
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		if x := strings.TrimSpace(parts[i]); net.ParseIP(x) != nil {
			return x
		}
	}
	host, _, e := net.SplitHostPort(r.RemoteAddr)
	if e == nil {
		return host
	}
	return r.RemoteAddr
}
func loginBlocked(ip string) time.Duration {
	loginLimiter.Lock()
	defer loginLimiter.Unlock()
	s := loginLimiter.Items[ip]
	if time.Now().Before(s.BlockedUntil) {
		return time.Until(s.BlockedUntil)
	}
	return 0
}
func recordLoginFailure(ip string) {
	loginLimiter.Lock()
	defer loginLimiter.Unlock()
	now := time.Now()
	if len(loginLimiter.Items) > 10000 {
		for key, item := range loginLimiter.Items {
			if now.Sub(item.WindowStart) > time.Hour && now.After(item.BlockedUntil) {
				delete(loginLimiter.Items, key)
			}
		}
	}
	s := loginLimiter.Items[ip]
	if now.Sub(s.WindowStart) > 15*time.Minute {
		s = loginState{WindowStart: now}
	}
	if s.WindowStart.IsZero() {
		s.WindowStart = now
	}
	s.Failures++
	if s.Failures >= 5 {
		s.BlockedUntil = now.Add(15 * time.Minute)
	}
	loginLimiter.Items[ip] = s
}
func clearLoginFailures(ip string) {
	loginLimiter.Lock()
	delete(loginLimiter.Items, ip)
	loginLimiter.Unlock()
}
func truncateText(v string, n int) string {
	if len(v) > n {
		return v[:n]
	}
	return v
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if strings.HasPrefix(env("PUBLIC_URL", ""), "https://") {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}
