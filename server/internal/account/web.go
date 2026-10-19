package account

// 网页注册：官网表单创建「TRON 地址 + 密码」账户，不绑定设备、不签发令牌。
// 用户随后在安卓 / Windows 客户端用同一地址和密码登录，登录路径会照常绑定那台设备。
//
// 不把浏览器当成一台设备，是为了不占设备名额，也不把刷新令牌交到静态页面上。
// 人机验证是自托管的工作量证明（SHA-256 前导零），不依赖在中国大陆打不开的第三方验证码。

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/auth"
	"github.com/yuhai-vpn/server/internal/db"
	"github.com/yuhai-vpn/server/internal/fieldenc"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/ids"
	"github.com/yuhai-vpn/server/internal/invite"
	"github.com/yuhai-vpn/server/internal/redisx"
)

const (
	webPoWAlgorithm   = "sha256-lzbits-v1"
	webPoWBits        = 16
	webPoWMaxCounter  = 1<<24 - 1
	webChallengeTTL   = 3 * time.Minute
	idemRegisterWeb   = "register_web"
	webRegisterOrigin = "https://yhvpn.io"
	webWWWOrigin      = "https://www.yhvpn.io"

	msgWebChallengeBad     = "验证未通过，请刷新页面后重试"
	msgWebChallengeExpired = "验证已过期，请刷新页面后重试"
	msgWebOriginDenied     = "请求被拒绝"
)

// WebChallenge 是发给浏览器的工作量证明题目。
// preimage = challenge_id + ":" + nonce + ":" + 十进制 counter（0 写成 "0"，无前导零）。
// digest = SHA-256(UTF-8(preimage))，其前 Difficulty 个比特必须为 0。
type WebChallenge struct {
	ChallengeID string    `json:"challenge_id"`
	Nonce       string    `json:"nonce"`
	Difficulty  int       `json:"difficulty"`
	Algorithm   string    `json:"algorithm"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type webChallengeRecord struct {
	Nonce     string    `json:"nonce"`
	Bits      int       `json:"bits"`
	IPHash    string    `json:"ip_hash"`
	ExpiresAt time.Time `json:"expires_at"`
}

// WebRegisterInput 是通过验证之后的注册材料。蜜罐和挑战不进业务层。
type WebRegisterInput struct {
	Address    string
	Password   string
	InviteCode string
}

// WebRegisterResult 不含令牌、不含设备。登录要到客户端去做。
type WebRegisterResult struct {
	AccountNo    string    `json:"account_no"`
	LoginAddress string    `json:"login_address"`
	InviteCode   string    `json:"invite_code"`
	InviteBound  bool      `json:"invite_bound"`
	ServerTime   time.Time `json:"server_time"`
}

type webRegisterBody struct {
	Address     string `json:"address"`
	Password    string `json:"password"`
	InviteCode  string `json:"invite_code"`
	Honeypot    string `json:"yh_hp"`
	ChallengeID string `json:"challenge_id"`
	Counter     int64  `json:"counter"`
}

func webChallengeKey(id string) string { return "webreg:v1:" + id }

func webOriginAllowed(origin string) bool {
	switch origin {
	case webRegisterOrigin, webWWWOrigin:
		return true
	default:
		return false
	}
}

func webPoWDigest(id, nonce string, counter int64) [32]byte {
	return sha256.Sum256([]byte(id + ":" + nonce + ":" + strconv.FormatInt(counter, 10)))
}

func leadingZeroBits(sum [32]byte) int {
	n := 0
	for _, b := range sum {
		if b == 0 {
			n += 8
			continue
		}
		for mask := byte(0x80); mask > 0; mask >>= 1 {
			if b&mask != 0 {
				return n
			}
			n++
		}
	}
	return n
}

func webPoWAccept(id, nonce string, counter int64, bits int) bool {
	if counter < 0 || counter > webPoWMaxCounter || bits <= 0 {
		return false
	}
	return leadingZeroBits(webPoWDigest(id, nonce, counter)) >= bits
}

// IssueWebChallenge 签发一道绑定当前客户端 IP 的题目。Redis 故障时拒绝，不降级成免验证。
func (s *Service) IssueWebChallenge(ctx context.Context, ip string) (WebChallenge, error) {
	if s == nil || s.Redis == nil || s.Clock == nil {
		return WebChallenge{}, httpx.NewError(httpx.ErrMaintenance, "系统繁忙，请稍后再试")
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return WebChallenge{}, err
	}
	id := uuid.NewString()
	nonce := base64.RawURLEncoding.EncodeToString(buf)
	now := s.Clock.Now()
	expires := now.Add(webChallengeTTL)
	ipHash, _ := hashIP(ip)
	rec := webChallengeRecord{Nonce: nonce, Bits: webPoWBits, IPHash: ipHash, ExpiresAt: expires}
	raw, err := json.Marshal(rec)
	if err != nil {
		return WebChallenge{}, err
	}
	if err := s.Redis.SetEX(ctx, webChallengeKey(id), string(raw), webChallengeTTL); err != nil {
		return WebChallenge{}, httpx.NewError(httpx.ErrMaintenance, "系统繁忙，请稍后再试").Wrap(err)
	}
	return WebChallenge{
		ChallengeID: id, Nonce: nonce, Difficulty: webPoWBits,
		Algorithm: webPoWAlgorithm, ExpiresAt: expires,
	}, nil
}

// VerifyWebChallenge 检查题目仍属于这个 IP，且 counter 确实满足难度。不删除题目。
func (s *Service) VerifyWebChallenge(ctx context.Context, id string, counter int64, ip string) error {
	if s == nil || s.Redis == nil || s.Clock == nil {
		return httpx.NewError(httpx.ErrMaintenance, "系统繁忙，请稍后再试")
	}
	if _, err := uuid.Parse(id); err != nil || counter < 0 || counter > webPoWMaxCounter {
		return httpx.NewError(httpx.ErrBadRequest, msgWebChallengeBad)
	}
	raw, err := s.Redis.Get(ctx, webChallengeKey(id))
	if errors.Is(err, redisx.ErrMissing) {
		return httpx.NewError(httpx.ErrBadRequest, msgWebChallengeExpired)
	} else if err != nil {
		return httpx.NewError(httpx.ErrMaintenance, "系统繁忙，请稍后再试").Wrap(err)
	}
	var rec webChallengeRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil || rec.Nonce == "" {
		return httpx.NewError(httpx.ErrBadRequest, msgWebChallengeBad)
	}
	ipHash, _ := hashIP(ip)
	if rec.Bits != webPoWBits || rec.IPHash != ipHash {
		return httpx.NewError(httpx.ErrBadRequest, msgWebChallengeBad)
	}
	if !s.Clock.Now().Before(rec.ExpiresAt) {
		return httpx.NewError(httpx.ErrBadRequest, msgWebChallengeExpired)
	}
	if !webPoWAccept(id, rec.Nonce, counter, rec.Bits) {
		return httpx.NewError(httpx.ErrBadRequest, msgWebChallengeBad)
	}
	return nil
}

// ConsumeWebChallenge 让这道题不能再注册第二个账户。删除失败不影响已经写入的账户。
func (s *Service) ConsumeWebChallenge(ctx context.Context, id string) {
	if s == nil || s.Redis == nil {
		return
	}
	if _, err := uuid.Parse(id); err != nil {
		return
	}
	_ = s.Redis.Del(ctx, webChallengeKey(id))
}

// RegisterFromWeb 创建地址账户。idemKey 必须是挑战 UUID：同一道题、同一请求体重复提交返回第一次的结果。
func (s *Service) RegisterFromWeb(ctx context.Context, in WebRegisterInput, idemKey string, rawBody []byte, ipHash, ipPrefixHash string) (WebRegisterResult, int, error) {
	address, err := normalizeLoginAddress(in.Address)
	if err != nil {
		return WebRegisterResult{}, 0, err
	}
	if err := validatePassword(in.Password); err != nil {
		return WebRegisterResult{}, 0, err
	}
	if _, err := uuid.Parse(idemKey); err != nil {
		return WebRegisterResult{}, 0, httpx.NewError(httpx.ErrBadRequest, msgWebChallengeBad)
	}
	pwHash, err := hashPassword(in.Password)
	if err != nil {
		return WebRegisterResult{}, 0, err
	}
	lookup := loginLookup(s.Pepper, address)
	var result WebRegisterResult
	for attempt := 0; attempt < 3; attempt++ {
		err = db.InTx(ctx, s.Repo.Pool, func(tx pgx.Tx) error {
			if _, e := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "register:"+idemKey); e != nil {
				return e
			}
			if _, e := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "login:"+lookup); e != nil {
				return e
			}
			var e error
			result, e = s.registerWebTx(ctx, tx, in, address, lookup, pwHash, idemKey, rawBody, ipHash, ipPrefixHash)
			return e
		})
		if !isUniqueViolation(err) {
			break
		}
	}
	if err != nil {
		return WebRegisterResult{}, 0, err
	}
	return result, http.StatusCreated, nil
}

func (s *Service) registerWebTx(ctx context.Context, tx pgx.Tx, in WebRegisterInput, address, lookup, pwHash, idemKey string, rawBody []byte, ipHash, ipPrefixHash string) (WebRegisterResult, error) {
	now := s.Clock.Now()
	reqHash := hashBytes(rawBody)
	var none WebRegisterResult
	if prev, err := s.Repo.getIdem(ctx, tx, idemUserRegister, idemKey, idemRegisterWeb); err != nil {
		return none, err
	} else if prev != nil {
		if prev.RequestHash != reqHash {
			return none, httpx.NewError(httpx.ErrConflict, "幂等键冲突")
		}
		if now.Sub(prev.CreatedAt) > idemRegisterTTL || len(prev.Response) <= 2 {
			return none, httpx.NewError(httpx.ErrNotFound, "注册记录已过期，请直接登录")
		}
		var encrypted []byte
		if err := json.Unmarshal(prev.Response, &encrypted); err != nil {
			return none, httpx.Internal.Wrap(err)
		}
		pt, err := s.Enc.Decrypt(encrypted, fieldenc.AAD("api_idempotency", "response", idemKey))
		if err != nil {
			return none, httpx.Internal.Wrap(err)
		}
		var res WebRegisterResult
		if err := json.Unmarshal(pt, &res); err != nil {
			return none, httpx.Internal.Wrap(err)
		}
		return res, nil
	}
	var taken bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE login_lookup=$1)`, lookup).Scan(&taken); err != nil {
		return none, err
	}
	if taken {
		return none, httpx.NewError(httpx.ErrAddressTaken, "这个地址已经注册过了，请直接登录")
	}
	res, err := s.insertWebUser(ctx, tx, in, address, lookup, pwHash, now, ipHash, ipPrefixHash)
	if err != nil {
		return none, err
	}
	pt, err := json.Marshal(res)
	if err != nil {
		return none, err
	}
	enc, err := s.Enc.Encrypt(pt, fieldenc.AAD("api_idempotency", "response", idemKey))
	if err != nil {
		return none, httpx.Internal.Wrap(err)
	}
	encoded, err := json.Marshal(enc)
	if err != nil {
		return none, err
	}
	if err := s.Repo.putIdem(ctx, tx, idemUserRegister, idemKey, idemRegisterWeb, reqHash, http.StatusCreated, encoded); err != nil {
		return none, httpx.Internal.Wrap(err)
	}
	return res, nil
}

func (s *Service) insertWebUser(ctx context.Context, tx pgx.Tx, in WebRegisterInput, address, lookup, pwHash string, now time.Time, ipHash, ipPrefixHash string) (WebRegisterResult, error) {
	var none WebRegisterResult
	accountNo, err := ids.NewAccountNo()
	if err != nil {
		return none, err
	}
	secret, err := ids.NewAccountSecret()
	if err != nil {
		return none, err
	}
	inviteCode, err := ids.NewInviteCode()
	if err != nil {
		return none, err
	}
	userID, err := s.Repo.insertUser(ctx, tx, accountNo, ids.SecretLookup(s.Pepper, secret), inviteCode, "WEB", ipHash, ipPrefixHash)
	if err != nil {
		return none, err
	}
	if err := s.Repo.initEntitlement(ctx, tx, userID); err != nil {
		return none, err
	}
	blob, err := s.encryptAddress(userID, address)
	if err != nil {
		return none, httpx.Internal.Wrap(err)
	}
	// 网页用户没有需要抄写的账户密钥，备份提醒直接视为已确认。lookup 是地址的 HMAC，不是明文。
	if _, err := tx.Exec(ctx, `UPDATE users SET login_lookup=$2, login_address_enc=$3, password_hash=$4,
 password_changed_at=$5, backup_acknowledged_at=$5, updated_at=$5 WHERE id=$1`,
		userID, lookup, blob, pwHash, now); err != nil {
		return none, err
	}
	bound := false
	code := strings.ToUpper(strings.TrimSpace(in.InviteCode))
	if code != "" && len(code) <= 12 {
		bindingTx, err := tx.Begin(ctx)
		if err != nil {
			return none, err
		}
		_, bindErr := invite.BindInTx(ctx, bindingTx, userID, invite.BindInput{Code: code, Method: "MANUAL"}, s.Clock)
		if bindErr == nil {
			if err := bindingTx.Commit(ctx); err != nil {
				return none, err
			}
			bound = true
		} else {
			if err := bindingTx.Rollback(ctx); err != nil {
				return none, err
			}
			var apiErr *httpx.Error
			if !errors.As(bindErr, &apiErr) || apiErr.Code.HTTPStatus() >= 500 {
				return none, bindErr
			}
		}
	}
	return WebRegisterResult{
		AccountNo: accountNo, LoginAddress: address, InviteCode: inviteCode, InviteBound: bound, ServerTime: now,
	}, nil
}

func webPreflightAllowed(c *gin.Context) bool {
	origin := c.GetHeader("Origin")
	if !webOriginAllowed(origin) {
		return false
	}
	if strings.ToUpper(strings.TrimSpace(c.GetHeader("Access-Control-Request-Method"))) != http.MethodPost {
		return false
	}
	raw := c.GetHeader("Access-Control-Request-Headers")
	if strings.TrimSpace(raw) == "" {
		return true
	}
	for _, h := range strings.Split(raw, ",") {
		h = strings.TrimSpace(strings.ToLower(h))
		if h == "" {
			continue
		}
		if h != "content-type" {
			return false
		}
	}
	return true
}

func webCORS() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Vary", "Origin")
		origin := c.GetHeader("Origin")
		if c.Request.Method == http.MethodOptions {
			if !webPreflightAllowed(c) {
				c.AbortWithStatus(http.StatusForbidden)
				return
			}
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Methods", "POST, OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Content-Type")
			c.Header("Access-Control-Max-Age", "600")
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		if origin != "" && !webOriginAllowed(origin) {
			httpx.Fail(c, httpx.NewError(httpx.ErrForbidden, msgWebOriginDenied))
			return
		}
		if webOriginAllowed(origin) {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Methods", "POST, OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Content-Type")
			c.Header("Access-Control-Max-Age", "600")
		}
		c.Next()
	}
}

func (h *Handler) mountWeb(v1 *gin.RouterGroup) {
	ip := func(c *gin.Context) string { return auth.ClientIP(c) }
	prefix := func(c *gin.Context) string { return ipPrefix(auth.ClientIP(c)) }
	web := v1.Group("/auth/web", webCORS())
	// OPTIONS 由 webCORS 直接结束；这里挂上路由，预检才不会落到 404。
	web.OPTIONS("/challenge", func(*gin.Context) {})
	web.OPTIONS("/register", func(*gin.Context) {})
	web.POST("/challenge",
		h.MW.RateLimit("web_challenge_ip", 30, time.Hour, true, ip),
		h.MW.RateLimit("web_challenge_prefix", 120, time.Hour, true, prefix),
		h.webChallenge)
	web.POST("/register",
		h.MW.RateLimit("register_ip", 5, time.Hour, true, ip),
		h.MW.RateLimit("register_prefix", 30, time.Hour, true, prefix),
		h.webRegister)
}

func (h *Handler) webChallenge(c *gin.Context) {
	res, err := h.Svc.IssueWebChallenge(c.Request.Context(), auth.ClientIP(c))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"data": res, "request_id": httpx.RequestID(c)})
}

func (h *Handler) webRegister(c *gin.Context) {
	var body webRegisterBody
	raw, ok := readJSON(c, &body)
	if !ok {
		return
	}
	if _, err := normalizeLoginAddress(body.Address); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := validatePassword(body.Password); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.Svc.VerifyWebChallenge(c.Request.Context(), body.ChallengeID, body.Counter, auth.ClientIP(c)); err != nil {
		httpx.Fail(c, err)
		return
	}
	if strings.TrimSpace(body.Honeypot) != "" {
		// 与真正注册一样先做一次慢哈希，避免用响应时间把蜜罐和验证失败区分开。
		_, _ = hashPassword(body.Password)
		h.Svc.ConsumeWebChallenge(c.Request.Context(), body.ChallengeID)
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, msgWebChallengeBad))
		return
	}
	ipHash, prefixHash := hashIP(auth.ClientIP(c))
	res, status, err := h.Svc.RegisterFromWeb(c.Request.Context(), WebRegisterInput{
		Address: body.Address, Password: body.Password, InviteCode: body.InviteCode,
	}, body.ChallengeID, raw, ipHash, prefixHash)
	h.Svc.ConsumeWebChallenge(c.Request.Context(), body.ChallengeID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(status, gin.H{"data": res, "request_id": httpx.RequestID(c)})
}
