package clientdiag

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yuhai-vpn/server/internal/auth"
	"github.com/yuhai-vpn/server/internal/httpx"
)

// Handler 接收客户端在连接失败时上报的诊断。需要登录并且带设备签名。
type Handler struct {
	Pool *pgxpool.Pool
	MW   *auth.Middleware
}

func (h *Handler) Mount(v1 *gin.RouterGroup) {
	dev := func(c *gin.Context) string { return strconv.FormatInt(httpx.DeviceID(c), 10) }
	v1.POST("/connect/diagnostics", h.MW.S2(), h.MW.RateLimit("connect_diag_dev", 10, time.Minute, false, dev), h.report)
}

type reportInput struct {
	Stage        string `json:"stage"`
	ErrorCode    string `json:"error_code"`
	ErrorText    string `json:"error_text"`
	ServiceState string `json:"service_state"`
	LogSummary   string `json:"log_summary"`
}

func (h *Handler) report(c *gin.Context) {
	if h.Pool == nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrMaintenance, "诊断暂不可用"))
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 8192))
	if err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "诊断内容无效"))
		return
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var in reportInput
	if err := dec.Decode(&in); err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "诊断内容无效"))
		return
	}
	row, err := Normalize(Report{
		UserID:       httpx.UserID(c),
		DeviceID:     httpx.DeviceID(c),
		AppVersion:   headerToken(c.GetHeader("X-App-Version"), 32),
		Platform:     headerToken(c.GetHeader("X-Platform"), 16),
		Stage:        in.Stage,
		ErrorCode:    in.ErrorCode,
		ErrorText:    in.ErrorText,
		ServiceState: in.ServiceState,
		LogSummary:   in.LogSummary,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := (&PG{Pool: h.Pool}).Insert(c.Request.Context(), row); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"accepted": true})
}

func headerToken(v string, n int) string {
	v = strings.TrimSpace(v)
	var b strings.Builder
	for _, r := range v {
		if r > 127 || unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
		if b.Len() >= n {
			break
		}
	}
	return b.String()
}
