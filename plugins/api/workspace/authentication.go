package workspace

import (
	"errors"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/security"
	"github.com/lengzhao/taskloom/runtime/httpx"
	"net"
	"net/http"
	"time"
)

type loginWindow struct {
	Until time.Time
	Count int
}

func (m *Module) admitLogin(r *http.Request) bool {
	host, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil {
		host = r.RemoteAddr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for key, v := range m.loginAttempts {
		if !v.Until.After(now) {
			delete(m.loginAttempts, key)
		}
	}
	v, exists := m.loginAttempts[host]
	if !exists {
		if len(m.loginAttempts) >= 10000 {
			return false
		}
		v = loginWindow{Until: now.Add(time.Minute)}
	}
	if v.Count >= 20 {
		return false
	}
	v.Count++
	m.loginAttempts[host] = v
	return true
}

// session logs in with a password only: the issued credential goes straight
// into the cookie. Logout revokes that credential, so it cannot be reused even
// if the cookie leaked. Credentials issued elsewhere never become sessions.
func (m *Module) session(w http.ResponseWriter, r *http.Request) {
	if r.Method == "DELETE" {
		if cookie, e := r.Cookie(httpx.SessionCookie); e == nil {
			if e = m.d.Security.Logout(r.Context(), cookie.Value); e != nil && !errors.Is(e, identity.ErrUnauthenticated) {
				httpx.Fail(w, e)
				return
			}
		}
		m.setSession(w, r, "", -1)
		httpx.Write(w, map[string]bool{"ok": true})
		return
	}
	if r.Header.Get("Authorization") != "" {
		httpx.Reject(w, 400, "password_login_only", "请使用用户名和密码登录")
		return
	}
	if !m.admitLogin(r) {
		w.Header().Set("Retry-After", "60")
		httpx.Reject(w, 429, "too_many_attempts", "登录尝试过多，请稍后重试")
		return
	}
	var in struct{ Username, Password string }
	if !httpx.Decode(w, r, &in) {
		return
	}
	issued, e := m.d.Security.Login(r.Context(), security.LoginInput{Tenant: m.c.LoginTenant, Username: in.Username, Password: in.Password})
	if e != nil {
		httpx.Reject(w, 401, "login_failed", "用户名或密码错误，或账户已停用")
		return
	}
	// The cookie lifetime is only a hint; the credential's own expiry decides.
	m.setSession(w, r, issued.Secret, int(time.Hour/time.Second))
	httpx.Write(w, map[string]bool{"ok": true})
}
