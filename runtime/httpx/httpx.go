package httpx

import (
	"encoding/json"
	"errors"
	"github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/identity"
	"io"
	"net"
	"net/http"
	"strings"
)

func Decode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		Fail(w, fault.ErrInput)
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		Fail(w, fault.ErrInput)
		return false
	}
	return true
}
func Write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func Fail(w http.ResponseWriter, e error) {
	status := 500
	msg := "service unavailable"
	switch {
	case errors.Is(e, identity.ErrUnauthenticated):
		status = 401
		msg = "请重新登录"
	case errors.Is(e, authorization.ErrForbidden):
		status = 403
		msg = "无权执行此操作"
	case errors.Is(e, fault.ErrNotFound):
		status = 404
		msg = "记录不存在"
	case errors.Is(e, fault.ErrConflict):
		status = 409
		msg = "版本或请求冲突，请刷新"
	case errors.Is(e, fault.ErrTooLarge):
		status = 413
		msg = "文件超过大小上限"
	case errors.Is(e, fault.ErrInput):
		status = 400
		msg = "输入或游标无效，请刷新同步"
	}
	http.Error(w, msg, status)
}

// SessionCookie names the browser session cookie. Its value is the credential
// a password login issued; the credential store alone decides its validity.
const SessionCookie = "taskloom_session"

// Credential returns the request's credential: a Bearer token, else the
// session cookie. The HTTP transport has already checked Origin and CSRF.
func Credential(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if c, e := r.Cookie(SessionCookie); e == nil {
		return c.Value
	}
	return ""
}

// LoopbackHost reports whether host (optionally with a port) names this
// machine. Loopback traffic never leaves it, so plain HTTP is acceptable there.
func LoopbackHost(host string) bool {
	if h, _, e := net.SplitHostPort(host); e == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
