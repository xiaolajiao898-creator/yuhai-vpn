package worldvpn

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ReleaseDeviceTx 在吊销设备的同一事务里，把这台设备尚未释放的批发 peer 标成待删除。
// 表还不存在（未跑 00046）时不阻断吊销。真正调用 deleteWireGuardPeer 由 worker 补做。
func ReleaseDeviceTx(ctx context.Context, tx pgx.Tx, userID, deviceID int64, now time.Time) error {
	if tx == nil || userID <= 0 || deviceID <= 0 {
		return nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	// 表不存在时这条语句会让整个事务进入失败状态。先建保存点，缺表就回滚到这里，吊销仍可提交。
	if _, err := tx.Exec(ctx, "SAVEPOINT worldvpn_release_peer"); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
UPDATE worldvpn_peers
   SET release_requested_at = COALESCE(release_requested_at, $3)
 WHERE user_id = $1 AND device_id = $2 AND released_at IS NULL`, userID, deviceID, now)
	if err != nil {
		_, _ = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT worldvpn_release_peer")
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
			return nil
		}
		return err
	}
	_, err = tx.Exec(ctx, "RELEASE SAVEPOINT worldvpn_release_peer")
	return err
}
