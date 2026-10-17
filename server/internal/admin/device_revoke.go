package admin

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/yuhai-vpn/server/internal/account"
	"github.com/yuhai-vpn/server/internal/httpx"
)

const deviceRevokeAction = "devices.revoke"

type DeviceRevokeService struct{ Security *ApprovalService }
type DeviceRevokeInput struct {
	OperationID string `json:"operation_id"`
	UserID      string `json:"user_id"`
	Reason      string `json:"reason"`
}
type ManagedDeviceDetail struct {
	UserID string        `json:"user_id"`
	Device ManagedDevice `json:"device"`
}
type DeviceRevokeResult struct {
	OperationID    string        `json:"operation_id"`
	UserID         string        `json:"user_id"`
	Device         ManagedDevice `json:"device"`
	AlreadyRevoked bool          `json:"already_revoked"`
	NodeSync       string        `json:"node_sync"`
}

func (in DeviceRevokeInput) valid() bool {
	id, err := uuid.Parse(in.OperationID)
	_, userErr := positiveApprovalID(in.UserID)
	return err == nil && id.Version() == 4 && id.Variant() == uuid.RFC4122 && id.String() == in.OperationID && userErr == nil && approvalText(in.Reason)
}

const managedDeviceColumns = `d.id::text,d.platform,d.display_name,d.status,d.created_at,d.last_seen_at,d.revoked_at`

func readManagedDevice(ctx context.Context, tx pgx.Tx, userID, deviceID int64) (d ManagedDevice, err error) {
	err = tx.QueryRow(ctx, `SELECT `+managedDeviceColumns+` FROM devices d WHERE d.user_id=$1 AND d.id=$2`, userID, deviceID).Scan(&d.ID, &d.Platform, &d.DisplayName, &d.Status, &d.CreatedAt, &d.LastSeenAt, &d.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, httpx.NewError(httpx.ErrNotFound, "指定用户的设备不存在")
	}
	if err == nil {
		err = validateManagedDevice(d)
	}
	return
}

func (s *UserReadService) Device(ctx context.Context, p Principal, userID, deviceID int64) (out ManagedDeviceDetail, err error) {
	if userID <= 0 || deviceID <= 0 {
		return out, httpx.NewError(httpx.ErrBadRequest, "用户或设备编号无效")
	}
	err = s.read(ctx, p, false, func(c context.Context, tx pgx.Tx) error {
		d, e := readManagedDevice(c, tx, userID, deviceID)
		if e != nil {
			return e
		}
		out = ManagedDeviceDetail{UserID: strconv.FormatInt(userID, 10), Device: d}
		return nil
	})
	if err != nil {
		return ManagedDeviceDetail{}, err
	}
	return out, nil
}

// An exact completed operation may reuse its original, still-valid proof for
// response recovery. That exception cannot authorize any new mutation, target,
// user, session or request bytes. Fresh proofs follow normal one-time consume.
func (s *DeviceRevokeService) replayProof(ctx context.Context, tx pgx.Tx, p ApprovalProof, sessionID int64, proofHash string) error {
	a := s.Security.Auth
	if tokenHash(p.Token) != proofHash || sessionID != p.Principal.SessionID {
		return a.ConsumeStepUp(ctx, tx, p.Principal, p.Token, deviceRevokeAction, p.RequestHash, p.IP)
	}
	var session int64
	var action, hash string
	var used *time.Time
	var expiry time.Time
	err := tx.QueryRow(ctx, `SELECT session_id,action_code,payload_hash,used_at,expires_at FROM admin_step_up_tokens WHERE token_hash=$1 FOR UPDATE`, proofHash).Scan(&session, &action, &hash, &used, &expiry)
	if errors.Is(err, pgx.ErrNoRows) {
		return errStepUp
	}
	if err != nil {
		return err
	}
	if session != p.Principal.SessionID || action != deviceRevokeAction || hash != p.RequestHash || used == nil || !expiry.After(a.Clock.Now()) {
		return errStepUp
	}
	return a.stepUpSessionFresh(ctx, tx, p.Principal)
}

// Lock order: admin user -> admin session/permissions -> proof -> owning app
// user -> refresh-device fence -> target device/credentials/sessions -> receipt
// and finally audit. Account restore/replacement use the same owning-user lock;
// refresh uses the same fence and never waits for admin locks.
func (s *DeviceRevokeService) Revoke(ctx context.Context, p ApprovalProof, deviceID int64, in DeviceRevokeInput) (out DeviceRevokeResult, err error) {
	if s == nil || s.Security == nil || s.Security.Auth == nil || s.Security.Auth.Pool == nil || s.Security.Auth.Clock == nil || s.Security.Audit == nil {
		return out, httpx.Internal
	}
	a := s.Security
	event := a.auditEvent(p, deviceRevokeAction, "device", deviceID)
	event.UserAgent = ""
	defer func() {
		if err != nil {
			out = DeviceRevokeResult{}
			err = a.RecordFailure(ctx, event, err)
		}
	}()
	if deviceID <= 0 || !in.valid() {
		return out, httpx.NewError(httpx.ErrBadRequest, "设备撤销资料无效")
	}
	if p.Token == "" {
		return out, httpx.NewError(httpx.ErrStepUpRequired, "请先完成二次验证")
	}
	if !validToken(p.Token) || !stepUpHash.MatchString(p.RequestHash) {
		return out, errStepUp
	}
	c, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	tx, err := a.Auth.Pool.BeginTx(c, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return out, err
	}
	defer rollbackApproval(tx)
	actor, err := a.Auth.lockStepUpAuthority(c, tx, p.Principal, deviceRevokeAction, p.IP)
	if err != nil {
		return out, err
	}
	if err = lockApprovalPermission(c, tx, actor.id, deviceRevokeAction, 2, a.Auth.Clock.Now()); err != nil {
		return out, err
	}
	var requestHash, proofHash string
	var oldUser, oldDevice, sessionID int64
	var receipt []byte
	err = tx.QueryRow(c, `SELECT request_hash,user_id,device_id,session_id,proof_hash,response FROM admin_device_mutations WHERE admin_user_id=$1 AND operation_id=$2`, actor.id, in.OperationID).Scan(&requestHash, &oldUser, &oldDevice, &sessionID, &proofHash, &receipt)
	userID, _ := positiveApprovalID(in.UserID)
	if err == nil {
		if requestHash != p.RequestHash || oldUser != userID || oldDevice != deviceID {
			return out, httpx.NewError(httpx.ErrConflict, "操作编号已用于不同请求")
		}
		if err = s.replayProof(c, tx, p, sessionID, proofHash); err != nil {
			return out, err
		}
		if err = json.Unmarshal(receipt, &out); err != nil {
			return out, err
		}
		if out.OperationID != in.OperationID || out.UserID != in.UserID || out.Device.ID != strconv.FormatInt(deviceID, 10) || out.Device.Status != "REVOKED" || (out.NodeSync != "QUEUED" && out.NodeSync != "NOT_REQUIRED") {
			return out, httpx.Internal
		}
		if err = validateManagedDevice(out.Device); err != nil {
			return out, err
		}
		if err = a.freshProof(c, tx, p); err != nil {
			return out, err
		}
		if err = tx.Commit(c); err != nil {
			return out, err
		}
		return out, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	err = nil
	if err = a.Auth.ConsumeStepUp(c, tx, p.Principal, p.Token, deviceRevokeAction, p.RequestHash, p.IP); err != nil {
		return out, err
	}
	event.StepUpVerified = true
	var target int64
	err = tx.QueryRow(c, `SELECT id FROM users WHERE id=$1 FOR NO KEY UPDATE`, userID).Scan(&target)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, httpx.NewError(httpx.ErrNotFound, "指定用户的设备不存在")
	}
	if err != nil {
		return out, err
	}
	// Refresh must finish before taking a device row lock, otherwise its FK
	// check for the rotated child session could deadlock with this transaction.
	if _, err = tx.Exec(c, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "refresh-device:"+strconv.FormatInt(deviceID, 10)); err != nil {
		return out, err
	}
	err = tx.QueryRow(c, `SELECT id FROM devices WHERE id=$1 AND user_id=$2 FOR UPDATE`, deviceID, userID).Scan(&target)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, httpx.NewError(httpx.ErrNotFound, "指定用户的设备不存在")
	}
	if err != nil {
		return out, err
	}
	before, err := readManagedDevice(c, tx, userID, deviceID)
	if err != nil {
		return out, err
	}
	out = DeviceRevokeResult{OperationID: in.OperationID, UserID: in.UserID, AlreadyRevoked: before.Status == "REVOKED", NodeSync: "NOT_REQUIRED"}
	now := a.Auth.Clock.Now()
	changed, e := account.RevokeDeviceInTx(c, tx, userID, deviceID, now)
	if e != nil {
		return out, e
	}
	if !out.AlreadyRevoked && !changed {
		return out, httpx.NewError(httpx.ErrConflict, "设备状态已变化，请核实")
	}
	// Legacy revocations may have no node event or may still have a surviving
	// session/credential. Ensure the latest desired node state is removal, once,
	// under the owning-user/device locks. Never infer a node acknowledgement.
	tag, e := tx.Exec(c, `INSERT INTO node_user_events(credential_id,effective_tier,reason,created_at)
	 SELECT c.id,0,'REVOKE',$2 FROM device_credentials c WHERE c.device_id=$1
	 AND COALESCE((SELECT e.effective_tier FROM node_user_events e WHERE e.credential_id=c.id ORDER BY e.id DESC LIMIT 1),-1)<>0`, deviceID, now)
	if e != nil {
		return out, e
	}
	if tag.RowsAffected() > 0 {
		out.NodeSync = "QUEUED"
	}
	if _, err = tx.Exec(c, `INSERT INTO device_bind_logs(user_id,device_id,action,operator_type,operator_id,idempotency_key,created_at) VALUES($1,$2,'REVOKE_BY_ADMIN','ADMIN',$3,$4,$5) ON CONFLICT(idempotency_key) DO NOTHING`, userID, deviceID, actor.id, "admin-revoke:"+strconv.FormatInt(deviceID, 10), now); err != nil {
		return out, err
	}
	out.Device, err = readManagedDevice(c, tx, userID, deviceID)
	if err != nil {
		return out, err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return out, err
	}
	if _, err = tx.Exec(c, `INSERT INTO admin_device_mutations(admin_user_id,operation_id,request_hash,user_id,device_id,session_id,proof_hash,response,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, actor.id, in.OperationID, p.RequestHash, userID, deviceID, p.Principal.SessionID, tokenHash(p.Token), raw, a.Auth.Clock.Now()); err != nil {
		return out, err
	}
	event.BeforeData, _ = json.Marshal(map[string]string{"target_id": before.ID, "status": before.Status})
	event.AfterData, _ = json.Marshal(map[string]string{"target_id": out.Device.ID, "status": out.Device.Status, "decision_note": in.Reason})
	if err = a.Audit.Append(c, tx, event); err != nil {
		return out, err
	}
	if err = a.freshProof(c, tx, p); err != nil {
		return out, err
	}
	if err = tx.Commit(c); err != nil {
		return out, err
	}
	return out, nil
}
