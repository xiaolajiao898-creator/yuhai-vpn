package nodecontrol

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/nodewire"
)

// Decode one flat JSON object, rejecting duplicate keys before unmarshalling.
// Pointer-free business DTOs remain safe because required/null are checked here.
func decodeHeartbeat(r *http.Request, w http.ResponseWriter) (Heartbeat, error) {
	var h Heartbeat
	media, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || r.URL.RawQuery != "" || r.URL.ForceQuery || r.Header.Get("Content-Encoding") != "" || len(r.Header.Values("Content-Type")) != 1 {
		return h, badHeartbeat()
	}
	for key, value := range params {
		if key != "charset" || !strings.EqualFold(value, "utf-8") {
			return h, badHeartbeat()
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	d := json.NewDecoder(r.Body)
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		return h, badHeartbeat()
	}
	fields := map[string]json.RawMessage{}
	allowed := map[string]bool{"sequence": true, "sent_at": true, "agent_version": true, "config_version": true, "snapshot_checksum": true, "online_count": true, "cpu": true, "mem": true, "net_rx_bps": true, "net_tx_bps": true, "kernel": true, "ready_protocols": true}
	for d.More() {
		tok, err = d.Token()
		if err != nil {
			return h, badHeartbeat()
		}
		key, ok := tok.(string)
		if !ok || !allowed[key] || fields[key] != nil {
			return h, badHeartbeat()
		}
		var raw json.RawMessage
		if err = d.Decode(&raw); err != nil || bytes.Equal(raw, []byte("null")) {
			return h, badHeartbeat()
		}
		fields[key] = raw
	}
	if tok, err = d.Token(); err != nil || tok != json.Delim('}') {
		return h, badHeartbeat()
	}
	if _, err = d.Token(); err != io.EOF {
		return h, badHeartbeat()
	}
	for key := range allowed {
		if key != "kernel" && key != "ready_protocols" && fields[key] == nil {
			return h, badHeartbeat()
		}
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return h, err
	}
	if err = json.Unmarshal(raw, &h); err != nil || (fields["kernel"] != nil && h.Kernel == "") {
		return h, badHeartbeat()
	}
	if _, err = validate(h); err != nil {
		return h, err
	}
	return h, nil
}

func (s *Service) Handler() http.Handler {
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	r.Use(httpx.RequestIDMiddleware(), httpx.RecoveryMiddleware(), httpx.AccessLogMiddleware())
	r.POST("/node/v1/heartbeat", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		if _, err := certificateIdentity(c.Request.TLS, s.Roots, s.Clock.Now()); err != nil {
			httpx.Fail(c, err)
			return
		}
		h, err := decodeHeartbeat(c.Request, c.Writer)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		out, err := s.Accept(c.Request.Context(), c.Request.TLS, h)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, out)
	})
	if s.EnrollCA != nil {
		// 注册是唯一不要求客户端证书的路由——节点此刻还没有证书。
		// 它的门是一次性令牌，不是 mTLS；因此这里显式**不**调用 certificateIdentity。
		// 监听器在启用注册时用 VerifyClientCertIfGiven：带了证书仍必须验得过，
		// 只是允许不带；其余路由各自的 certificateIdentity 依然失败闭合。
		r.POST("/node/v1/enroll", func(c *gin.Context) {
			c.Header("Cache-Control", "no-store")
			raw, e := decodeEnrollBody(c.Request, c.Writer)
			if e != nil {
				httpx.Fail(c, e)
				return
			}
			out, e := s.Enroll(c.Request.Context(), raw)
			if e != nil {
				httpx.Fail(c, e)
				return
			}
			httpx.OK(c, out)
		})
	}
	if s.EnrollCA != nil {
		// 续期与注册相反：这里**必须**有有效客户端证书，身份完全来自 mTLS。
		// 它只能延长已有身份的寿命，拿不到新身份。
		r.POST("/node/v1/cert/renew", func(c *gin.Context) {
			c.Header("Cache-Control", "no-store")
			if _, e := certificateIdentity(c.Request.TLS, s.Roots, s.Clock.Now()); e != nil {
				httpx.Fail(c, e)
				return
			}
			raw, e := decodeEnrollBody(c.Request, c.Writer)
			if e != nil {
				httpx.Fail(c, e)
				return
			}
			out, e := s.Renew(c.Request.Context(), c.Request.TLS, raw)
			if e != nil {
				httpx.Fail(c, e)
				return
			}
			httpx.OK(c, out)
		})
	}
	r.GET("/node/v1/config", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		req := c.Request
		raw, e := io.ReadAll(io.LimitReader(req.Body, 1))
		if e != nil || len(raw) != 0 || req.URL.RawQuery != "" || req.URL.ForceQuery {
			httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "配置请求无效"))
			return
		}
		out, e := s.Config(req.Context(), req.TLS)
		if e != nil {
			httpx.Fail(c, e)
			return
		}
		httpx.OK(c, out)
	})
	if s.WireGuardManifestEnabled {
		r.GET("/node/v1/wireguard/manifest", func(c *gin.Context) {
			c.Header("Cache-Control", "no-store")
			if _, e := certificateIdentity(c.Request.TLS, s.Roots, s.Clock.Now()); e != nil {
				httpx.Fail(c, e)
				return
			}
			req := c.Request
			raw, e := io.ReadAll(io.LimitReader(req.Body, 1))
			if e != nil || len(raw) != 0 || req.URL.RawQuery != "" || req.URL.ForceQuery || req.Header.Get("Content-Encoding") != "" || len(req.Header.Values("X-Snapshot-Nonce")) != 1 || !nodewire.ValidWGNonce(req.Header.Get("X-Snapshot-Nonce")) {
				httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "清单请求无效"))
				return
			}
			out, e := s.WireGuardManifest(req.Context(), req.TLS, req.Header.Get("X-Snapshot-Nonce"))
			if e != nil {
				httpx.Fail(c, e)
				return
			}
			httpx.OK(c, out)
		})
	}
	if s.TrafficReportEnabled {
		r.POST("/node/v1/traffic", func(c *gin.Context) {
			c.Header("Cache-Control", "no-store")
			if _, e := certificateIdentity(c.Request.TLS, s.Roots, s.Clock.Now()); e != nil {
				httpx.Fail(c, e)
				return
			}
			raw, e := decodeTrafficBody(c.Request, c.Writer)
			if e != nil {
				httpx.Fail(c, e)
				return
			}
			out, e := s.AcceptTraffic(c.Request.Context(), c.Request.TLS, raw)
			if e != nil {
				httpx.Fail(c, e)
				return
			}
			httpx.OK(c, out)
		})
	}
	return r
}

// decodeTrafficBody 只做传输层检查；字段与数值的严格校验交给 nodewire.ParseTrafficReport，
// 保证线上解析与单测解析是同一份实现。
func decodeTrafficBody(r *http.Request, w http.ResponseWriter) ([]byte, error) {
	media, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || r.URL.RawQuery != "" || r.URL.ForceQuery || r.Header.Get("Content-Encoding") != "" || len(r.Header.Values("Content-Type")) != 1 {
		return nil, badTrafficRequest()
	}
	for key, value := range params {
		if key != "charset" || !strings.EqualFold(value, "utf-8") {
			return nil, badTrafficRequest()
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, 262144)
	raw, err := io.ReadAll(r.Body)
	if err != nil || len(raw) == 0 {
		return nil, badTrafficRequest()
	}
	return raw, nil
}

// decodeEnrollBody 与心跳同一套严格读取：只接受 application/json、无查询串、无内容编码。
func decodeEnrollBody(r *http.Request, w http.ResponseWriter) ([]byte, error) {
	media, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || r.URL.RawQuery != "" || r.URL.ForceQuery ||
		r.Header.Get("Content-Encoding") != "" || len(r.Header.Values("Content-Type")) != 1 {
		return nil, badEnroll()
	}
	for key, value := range params {
		if key != "charset" || !strings.EqualFold(value, "utf-8") {
			return nil, badEnroll()
		}
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32*1024))
	if err != nil {
		return nil, badEnroll()
	}
	return raw, nil
}
