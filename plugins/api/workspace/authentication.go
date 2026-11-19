package workspace

import (
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
func (m *Module) loginToken(w http.ResponseWriter, r *http.Request, token string) (string, error) {
	if !m.admitLogin(r) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "登录尝试过多，请稍后重试", 429)
		return "", errDecoded
	}
	if r.Header.Get("Authorization") != "" {
		p, e := m.d.Identity.Authenticate(r.Context(), token)
		if e != nil {
			return "", e
		}
		view, e := m.d.Queries.Viewer(r.Context(), token)
		if e != nil {
			return "", e
		}
		if p.Kind == "human" {
			exists, e := m.d.Security.HasPassword(r.Context(), p.Tenant, p.ID)
			if e != nil {
				return "", e
			}
			if !view.CanAdmin || exists {
				return "", identity.ErrUnauthenticated
			}
		} else if p.Kind != "agent" {
			return "", identity.ErrUnauthenticated
		}
		return token, nil
	}
	var in struct{ Username, Password string }
	if !httpx.Decode(w, r, &in) {
		return "", errDecoded
	}
	issued, e := m.d.Security.Login(r.Context(), security.LoginInput{Tenant: m.c.LoginTenant, Username: in.Username, Password: in.Password})
	if e != nil {
		http.Error(w, "用户名或密码错误，或账户已停用", 401)
		return "", errDecoded
	}
	return issued.Secret, nil
}
