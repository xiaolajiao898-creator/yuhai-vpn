package admin

import (
	"context"
	"regexp"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/yuhai-vpn/server/internal/httpx"
)

// 撤销执行回执的只读视图。
//
// confirmed 为真的唯一含义：该节点上报的运行状态校验和与控制面重算的期望清单一致，
// 且该设备的全部凭据都不在其中——即「该节点上已不存在这些凭据」。
//
// confirmed 为假**不表示撤销失败，也不表示凭据仍在生效**。节点离线、没上报校验和、
// 或尚未收敛，都会落在这一侧。因此本视图返回全部节点而不是只返回已确认的那些：
// 只列已确认的节点会让人误以为覆盖完整。
type DeviceRevocationReceipt struct {
	NodeID            string     `json:"node_id"`
	NodeName          string     `json:"node_name"`
	NodeStatus        string     `json:"node_status"`
	Confirmed         bool       `json:"confirmed"`
	ConfirmedAbsentAt *time.Time `json:"confirmed_absent_at"`
	HeartbeatSequence *string    `json:"heartbeat_sequence"`
	SnapshotChecksum  *string    `json:"snapshot_checksum"`
}

type DeviceRevocationReceipts struct {
	UserID             string                    `json:"user_id"`
	DeviceID           string                    `json:"device_id"`
	DeviceStatus       string                    `json:"device_status"`
	CredentialCount    string                    `json:"credential_count"`
	ConfirmedNodeCount string                    `json:"confirmed_node_count"`
	Items              []DeviceRevocationReceipt `json:"items"`
}

var receiptChecksum = regexp.MustCompile(`^[a-f0-9]{64}$`)

func validateReceipt(r DeviceRevocationReceipt) error {
	if _, err := positiveApprovalID(r.NodeID); err != nil {
		return httpx.Internal
	}
	switch r.NodeStatus {
	case "ONLINE", "OFFLINE", "MAINTENANCE", "DISABLED":
	default:
		return httpx.Internal
	}
	if !approvalText(r.NodeName) {
		return httpx.Internal
	}
	// 未确认的节点不得携带任何确认证据；已确认的必须三样齐备，缺一即视为内部不一致。
	present := r.ConfirmedAbsentAt != nil && r.HeartbeatSequence != nil && r.SnapshotChecksum != nil
	if r.Confirmed != present {
		return httpx.Internal
	}
	if present {
		if _, err := positiveApprovalID(*r.HeartbeatSequence); err != nil {
			return httpx.Internal
		}
		if !receiptChecksum.MatchString(*r.SnapshotChecksum) {
			return httpx.Internal
		}
	}
	return nil
}

// Receipts 返回该设备的撤销在各节点上的确认情况。只读，不下发任何配置。
func (s *UserReadService) Receipts(ctx context.Context, p Principal, userID, deviceID int64) (out DeviceRevocationReceipts, err error) {
	if userID <= 0 || deviceID <= 0 {
		return out, httpx.NewError(httpx.ErrBadRequest, "用户或设备编号无效")
	}
	// 回执是独立能力：users.read 之外还必须持有 devices.receipts，两者同一快照核验。
	err = s.readWith(ctx, p, []string{"users.read", "devices.receipts"}, func(c context.Context, tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(c, `SELECT status FROM devices WHERE id=$1 AND user_id=$2`, deviceID, userID).Scan(&status)
		if err == pgx.ErrNoRows {
			return httpx.NewError(httpx.ErrNotFound, "设备不存在")
		}
		if err != nil {
			return err
		}
		if status != "ACTIVE" && status != "REVOKED" {
			return httpx.Internal
		}
		var credentials int64
		if err := tx.QueryRow(c, `SELECT count(*) FROM device_credentials WHERE device_id=$1`, deviceID).Scan(&credentials); err != nil {
			return err
		}
		// 某节点算「已确认」的条件：该设备的每一条凭据在该节点上都有回执。
		// device_credentials.device_id 唯一，当前一台设备只有一条凭据，因此这里实际是
		// 0 或 1；按「全部覆盖」判定是为了将来换发历史出现时语义仍然正确。
		// 设备没有凭据时任何节点都无从确认，confirmed 一律为假。
		rows, err := tx.Query(c, `
 WITH creds AS (SELECT id FROM device_credentials WHERE device_id=$1),
 confirmed AS (
   SELECT r.node_id, count(*) AS covered, max(r.confirmed_absent_at) AS at,
          max(r.heartbeat_sequence) AS sequence, max(r.snapshot_checksum) AS checksum
     FROM node_credential_receipts r
    WHERE r.credential_id IN (SELECT id FROM creds)
    GROUP BY r.node_id)
 SELECT n.id::text, n.name, n.status,
        COALESCE(cf.covered,0) = $2::bigint AND $2::bigint > 0 AS confirmed,
        cf.at, cf.sequence::text, cf.checksum
   FROM nodes n LEFT JOIN confirmed cf ON cf.node_id = n.id
  ORDER BY n.id LIMIT 200`, deviceID, credentials)
		if err != nil {
			return err
		}
		defer rows.Close()
		out = DeviceRevocationReceipts{
			UserID:          strconv.FormatInt(userID, 10),
			DeviceID:        strconv.FormatInt(deviceID, 10),
			DeviceStatus:    status,
			CredentialCount: strconv.FormatInt(credentials, 10),
			Items:           []DeviceRevocationReceipt{},
		}
		var confirmedNodes int64
		for rows.Next() {
			var r DeviceRevocationReceipt
			if err := rows.Scan(&r.NodeID, &r.NodeName, &r.NodeStatus, &r.Confirmed, &r.ConfirmedAbsentAt, &r.HeartbeatSequence, &r.SnapshotChecksum); err != nil {
				return err
			}
			// 部分覆盖（只有一部分凭据有回执）不算确认，也不得展示半截证据。
			if !r.Confirmed {
				r.ConfirmedAbsentAt, r.HeartbeatSequence, r.SnapshotChecksum = nil, nil, nil
			}
			if err := validateReceipt(r); err != nil {
				return err
			}
			if r.Confirmed {
				confirmedNodes++
			}
			out.Items = append(out.Items, r)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		out.ConfirmedNodeCount = strconv.FormatInt(confirmedNodes, 10)
		return nil
	})
	if err != nil {
		return DeviceRevocationReceipts{}, err
	}
	return out, nil
}
