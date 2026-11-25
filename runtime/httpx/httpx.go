package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/workflow"
	"io"
	"net"
	"net/http"
	"strings"
)

// Decode reads one JSON object with no unknown fields; on failure it answers
// the request and returns false.
func Decode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		Fail(w, decodeError(err))
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		Fail(w, fault.Input(fault.CodeMalformedBody, ""))
		return false
	}
	return true
}

func decodeError(err error) error {
	var tooLarge *http.MaxBytesError
	var typeErr *json.UnmarshalTypeError
	msg := err.Error()
	switch {
	case errors.As(err, &tooLarge):
		return fault.Coded(fault.ErrTooLarge, "body_too_large")
	case errors.As(err, &typeErr):
		return fault.Input(fault.CodeInvalid, typeErr.Field)
	case strings.HasPrefix(msg, "json: unknown field "):
		return fault.Input(fault.CodeUnknownField, strings.Trim(strings.TrimPrefix(msg, "json: unknown field "), `"`))
	}
	return fault.Input(fault.CodeMalformedBody, "")
}

// View answers a successful task write with the caller's current view of
// taskID, the same as GET /tasks/{id}. The receipt keeps only the command's
// reference, so a replay also shows current facts. A write that leaves the
// caller unable to read the task (moving it out of the caller's project, for
// example) answers 204.
func View(ctx context.Context, w http.ResponseWriter, views workflow.Queries, token, taskID string, err error) {
	if err != nil {
		Fail(w, err)
		return
	}
	v, err := views.Read(ctx, token, taskID, 0)
	if errors.Is(err, authorization.ErrForbidden) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		Fail(w, err)
		return
	}
	Write(w, v)
}

func Write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// Problem is every error response body: a stable Code for programs, a short
// Message for people and, when known, the request Field at fault.
type Problem struct {
	Code, Message string
	Field         string `json:",omitempty"`
}

// Reject answers with a Problem the caller can act on, for checks made
// before any command runs (origin, login).
func Reject(w http.ResponseWriter, status int, code, message string) {
	writeProblem(w, status, Problem{Code: code, Message: message})
}
func writeProblem(w http.ResponseWriter, status int, p Problem) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(p)
}

func Fail(w http.ResponseWriter, e error) {
	status, p := 500, Problem{Code: "internal", Message: "服务暂不可用"}
	switch {
	case errors.Is(e, identity.ErrUnauthenticated):
		status, p = 401, Problem{Code: "unauthenticated", Message: "请重新登录"}
	case errors.Is(e, authorization.ErrForbidden):
		status, p = 403, Problem{Code: "forbidden", Message: "无权执行此操作"}
	case errors.Is(e, fault.ErrNotFound):
		status, p = 404, Problem{Code: "not_found", Message: "记录不存在"}
	case errors.Is(e, fault.ErrConflict):
		status, p = 409, Problem{Code: "conflict", Message: "当前状态不允许此操作，请刷新"}
	case errors.Is(e, fault.ErrTooLarge):
		status, p = 413, Problem{Code: "too_large", Message: "内容超过大小上限"}
	case errors.Is(e, fault.ErrInput):
		status, p = 400, Problem{Code: "invalid_input", Message: "输入无效"}
	}
	var coded *fault.Error
	if status != 500 && errors.As(e, &coded) {
		p.Code, p.Field = coded.Code, coded.Field
		if m := fault.Message(coded.Code); m != "" {
			p.Message = m
		}
	}
	writeProblem(w, status, p)
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
