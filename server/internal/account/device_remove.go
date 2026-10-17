package account

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/yuhai-vpn/server/internal/auth"
	"github.com/yuhai-vpn/server/internal/db"
	"github.com/yuhai-vpn/server/internal/httpx"
)

func (h *Handler) removeDevice(c *gin.Context) {
	rawID := c.Param("device_id")
	deviceID, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || deviceID <= 0 || strconv.FormatInt(deviceID, 10) != rawID {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "device_id 无效"))
		return
	}
	if _, err := uuid.Parse(c.GetHeader("Idempotency-Key")); err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "Idempotency-Key 必须是 UUID"))
		return
	}
	ipHash, _ := hashIP(auth.ClientIP(c))
	if err := h.Svc.RemoveDevice(c.Request.Context(), httpx.UserID(c), httpx.DeviceID(c), deviceID, ipHash); err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusNoContent)
}

// RemoveDevice 移除本账户的其他设备。账户行锁与恢复/替换共用，且在事务内
// 吊销设备、连接凭证、所有登录会话并追加唯一 revoke:{device_id} 日志。
// 已移除的本账户设备保持成功，其他账户和不存在的设备保持相同的 404。
func (s *Service) RemoveDevice(ctx context.Context, userID, currentDeviceID, deviceID int64, ipHash string) error {
	if userID <= 0 || currentDeviceID <= 0 {
		return httpx.NewError(httpx.ErrTokenMissing, "访问令牌无效")
	}
	if deviceID <= 0 {
		return httpx.NewError(httpx.ErrBadRequest, "device_id 无效")
	}
	if deviceID == currentDeviceID {
		return httpx.NewError(httpx.ErrBadRequest, "当前设备请使用退出登录")
	}
	return db.InTx(ctx, s.Repo.Pool, func(tx pgx.Tx) error {
		var userStatus string
		err := tx.QueryRow(ctx, `SELECT status FROM users WHERE id=$1 FOR NO KEY UPDATE`, userID).Scan(&userStatus)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewError(httpx.ErrTokenMissing, "访问令牌无效")
		}
		if err != nil {
			return err
		}
		if userStatus == "BANNED" {
			return httpx.NewError(httpx.ErrAccountDisabled, "账户已停用")
		}
		// 重新检查发起设备，避免两个设备同时互相移除，或排队中的请求在
		// 发起设备已经失效后继续执行。行锁也阻止并发退出穿过本次检查。
		var currentStatus string
		err = tx.QueryRow(ctx, `SELECT status FROM devices WHERE id=$1 AND user_id=$2 FOR SHARE`, currentDeviceID, userID).Scan(&currentStatus)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && currentStatus != "ACTIVE") {
			return httpx.NewError(httpx.ErrDeviceRevoked, "此设备已被移除")
		}
		if err != nil {
			return err
		}
		var targetStatus string
		err = tx.QueryRow(ctx, `SELECT status FROM devices WHERE id=$1 AND user_id=$2`, deviceID, userID).Scan(&targetStatus)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewError(httpx.ErrNotFound, "要移除的设备不存在")
		}
		if err != nil {
			return err
		}
		if targetStatus == "REVOKED" {
			return nil
		}
		// 刷新会话共用此锁，先等待正在进行的刷新提交，再一次性吊销所有
		// 后代会话；否则 UPDATE 的快照可能漏掉并发刚插入的刷新令牌。
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "refresh-device:"+strconv.FormatInt(deviceID, 10)); err != nil {
			return err
		}
		now := s.Clock.Now()
		if _, err := s.Repo.revokeDevice(ctx, tx, userID, deviceID, now); err != nil {
			return err
		}
		return s.Repo.insertBindLog(ctx, tx, userID, deviceID, "REVOKE_BY_USER", "USER", ipHash, "revoke:"+strconv.FormatInt(deviceID, 10))
	})
}
