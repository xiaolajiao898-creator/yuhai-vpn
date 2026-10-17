package account

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/yuhai-vpn/server/internal/auth"
	"github.com/yuhai-vpn/server/internal/db"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/nodewire"
)

// PUT /v1/devices/current/wg-key —— 设备上传自己的 WireGuard 公钥（方案 11.3）。
//
// 这是「设备能不能连上」的第一步：没有这一步，device_credentials.wg_pubkey 与
// wg_address 永远是空的，控制面清单里就不会有这台设备，/v1/connect/profiles 也无材料可发。
//
// 四条边界：
//
//  1. 私钥永不上传，服务端只见公钥；公钥用与节点侧完全相同的一份实现校验
//     （nodewire.ValidWGPublicKey），避免两边口径不一致导致下发出无效 peer。
//  2. 公钥全局唯一。一把公钥被另一台设备占用时返回 40901，而**不是**悄悄顶掉对方——
//     顶掉意味着另一台设备的流量会被引到本设备的隧道地址上。
//  3. 隧道地址一旦分配就跟着这条凭据走，轮换公钥不换地址；设备被移除后地址**不回收**。
//     回收会带来一个很难排查的故障：新设备拿到旧地址，而某个节点上还留着旧 peer 时，
//     这部分流量会被送到错误的对端。宁可让地址池单调消耗，也不做回收。
//  4. 地址与公钥在同一条 UPDATE 里写入，不会出现「有公钥没地址」的中间态——
//     控制面清单遇到这种半截记录会整体拒绝下发（nodecontrol.wgDesiredState），
//     那会连累这个节点上的所有用户。
type WGKeyInput struct {
	PubKey string `json:"wg_pubkey"`
}

// wgAllocLock 把地址分配串行化。分配本身靠 wg_address 的唯一约束兜底，
// 这把锁只是为了避免并发时反复撞约束重试。
const wgAllocLock = "wg-address-alloc:v1"

// SetWireGuardKey 保存本设备公钥，必要时分配隧道地址。重复上传同一把公钥不写库。
func (s *Service) SetWireGuardKey(ctx context.Context, deviceID int64, in WGKeyInput) error {
	if !nodewire.ValidWGPublicKey(in.PubKey) {
		return httpx.NewError(httpx.ErrBadRequest, "wg_pubkey 必须是 Base64 编码的 32 字节公钥")
	}
	if s.WGPool == nil {
		return httpx.NewError(httpx.ErrInternal, "隧道地址池未配置")
	}
	now := s.Clock.Now()
	return db.InTx(ctx, s.Repo.Pool, func(tx pgx.Tx) error {
		var deviceStatus, credStatus string
		var credID int64
		var current, address *string
		// 只锁凭据行：设备撤销同样要改这一行（置为 REVOKED），因此这把锁足以把两边串起来，
		// 而锁 devices 会额外要求对 devices 的 UPDATE 权限——这个接口本不该有。
		err := tx.QueryRow(ctx, `SELECT d.status,c.id,c.status,c.wg_pubkey,c.wg_address::text
 FROM devices d JOIN device_credentials c ON c.device_id=d.id WHERE d.id=$1 FOR UPDATE OF c`, deviceID).
			Scan(&deviceStatus, &credID, &credStatus, &current, &address)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewError(httpx.ErrDeviceRevoked, "设备已被移除")
		}
		if err != nil {
			return err
		}
		if deviceStatus != "ACTIVE" || credStatus != "ACTIVE" {
			return httpx.NewError(httpx.ErrDeviceRevoked, "设备已被移除")
		}
		// 已经是这把公钥：不写库，也不动 rotated_at——重复上传不该看起来像一次轮换。
		if current != nil && *current == in.PubKey && address != nil {
			return nil
		}
		next := address
		if next == nil {
			allocated, e := s.allocateWGAddress(ctx, tx)
			if e != nil {
				return e
			}
			next = &allocated
		}
		tag, err := tx.Exec(ctx, `UPDATE device_credentials SET wg_pubkey=$1,wg_address=$2::inet,rotated_at=$3
 WHERE id=$4 AND status='ACTIVE'`, in.PubKey, *next, now, credID)
		if err == nil && tag.RowsAffected() != 1 {
			// 行锁已经把撤销串行化了，走到这里说明有别的路径在绕过它改这张表。
			return httpx.NewError(httpx.ErrDeviceRevoked, "设备已被移除")
		}
		if err != nil {
			var pg *pgconn.PgError
			if errors.As(err, &pg) && pg.Code == "23505" {
				switch pg.ConstraintName {
				case "device_credentials_wg_pubkey_key":
					return httpx.NewError(httpx.ErrConflict, "该公钥已被另一台设备占用")
				case "device_credentials_wg_address_key":
					// 分配锁之外还有唯一约束兜底；撞上说明有人绕过了这条路径写地址。
					return httpx.NewError(httpx.ErrWGAddressExhausted, "隧道地址分配冲突").Wrap(err)
				}
			}
			return err
		}
		return nil
	})
}

// allocateWGAddress 取池内下一个从未分配过的地址。
// 只按「最大已用偏移 + 1」前进，不去填被移除设备留下的空洞——见本文件开头第 3 条。
func (s *Service) allocateWGAddress(ctx context.Context, tx pgx.Tx) (string, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, wgAllocLock); err != nil {
		return "", err
	}
	base := s.WGPool.IP.Mask(s.WGPool.Mask).String()
	ones, bits := s.WGPool.Mask.Size()
	size := int64(1) << uint(bits-ones)
	var used *int64
	err := tx.QueryRow(ctx, `SELECT max(wg_address - $1::inet) FROM device_credentials WHERE wg_address <<= $2::inet`,
		base, s.WGPool.String()).Scan(&used)
	if err != nil {
		return "", err
	}
	next := int64(s.WGReserved)
	if used != nil && *used >= next {
		next = *used + 1
	}
	// size-1 是广播地址，不分配。
	if next > size-2 {
		return "", httpx.NewError(httpx.ErrWGAddressExhausted, "隧道地址池已耗尽")
	}
	var addr string
	if err := tx.QueryRow(ctx, `SELECT host($1::inet + $2::bigint)`, base, next).Scan(&addr); err != nil {
		return "", err
	}
	// 下发给客户端与节点的都是 /32：一台设备只占自己这一个地址。
	peer := addr + "/32"
	// 用节点侧同一份校验兜底：宁可这里报错，也不要让一个不合法的地址进库，
	// 再由它把整个节点的清单拖成「不完整」而全员断线。
	if !nodewire.ValidWGPeerIP(peer) {
		return "", httpx.NewError(httpx.ErrWGAddressExhausted, "隧道地址池配置无效").
			Wrap(errors.New("allocated address rejected by peer validator: " + peer))
	}
	return peer, nil
}

func (h *Handler) putWGKey(c *gin.Context) {
	body, err := auth.ReadBody(c)
	if err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "请求体过大"))
		return
	}
	var in WGKeyInput
	if err := json.Unmarshal(body, &in); err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "参数格式错误"))
		return
	}
	if _, err := uuid.Parse(c.GetHeader("Idempotency-Key")); err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "Idempotency-Key 必须是 UUID"))
		return
	}
	if err := h.Svc.SetWireGuardKey(c.Request.Context(), httpx.DeviceID(c), in); err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusNoContent)
}
