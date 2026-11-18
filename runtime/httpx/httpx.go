package httpx

import (
	"encoding/json"
	"errors"
	"github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/identity"
	"io"
	"net/http"
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
	case errors.Is(e, fault.ErrInput):
		status = 400
		msg = "输入或游标无效，请刷新同步"
	}
	http.Error(w, msg, status)
}
