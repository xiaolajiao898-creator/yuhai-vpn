package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/httpx"
)

type Event struct {
	AdminUserID                                              *int64
	ActionCode, TargetType, RequestID, IP, UserAgent, Result string
	TargetID, ApprovalID                                     *int64
	BeforeData, AfterData                                    json.RawMessage
	StepUpVerified                                           bool
}

type Service struct {
	Pool  *pgxpool.Pool
	Clock clock.Clock
}

// Entry intentionally excludes snapshots, IP and user agent. Audit browsing is
// metadata-only; a future privileged detail endpoint must review disclosure.
type Entry struct {
	ID             string  `json:"id"`
	AdminUserID    *string `json:"admin_user_id"`
	ActionCode     string  `json:"action_code"`
	TargetType     *string `json:"target_type"`
	TargetID       *string `json:"target_id"`
	RequestID      string  `json:"request_id"`
	Result         string  `json:"result"`
	StepUpVerified bool    `json:"step_up_verified"`
	ApprovalID     *string `json:"approval_id"`
	PrevHash       string  `json:"prev_hash"`
	RowHash        string  `json:"row_hash"`
	CreatedAt      string  `json:"created_at"`
}

type ListResult struct {
	Items    []Entry `json:"items"`
	Page     int     `json:"page"`
	PageSize int     `json:"page_size"`
	Total    string  `json:"total"`
}

type VerifyResult struct {
	Valid          bool    `json:"valid"`
	FromID         string  `json:"from_id"`
	ToID           string  `json:"to_id"`
	Checked        int     `json:"checked"`
	HeadID         string  `json:"head_id"`
	HeadHash       string  `json:"head_hash"`
	FirstBrokenID  *string `json:"first_broken_id"`
	Reason         string  `json:"reason"`
	ExternalAnchor bool    `json:"external_anchor"`
}

type record struct {
	Event
	ID                        int64
	PrevHash, RowHash         string
	CreatedAt                 time.Time
	invalidNullRepresentation bool
}

const timestampLayout = "2006-01-02T15:04:05.000000Z"
const auditLock = "admin-audit-chain:v1"
const maxVerifyRows = 10000
const recordColumns = `id,admin_user_id,action_code,target_type,target_id,request_id,CASE WHEN ip IS NULL THEN NULL WHEN (family(ip)=4 AND masklen(ip)=32) OR (family(ip)=6 AND masklen(ip)=128) THEN host(ip) ELSE 'INVALID_MASK' END,user_agent,before_data,after_data,result,step_up_verified,approval_id,prev_hash,row_hash,created_at`

var ErrInvalidEvent = errors.New("audit: invalid or unsafe event metadata")
var ErrChain = errors.New("audit: existing chain head is invalid; transaction must roll back")

func (s *Service) Append(ctx context.Context, tx pgx.Tx, e Event) error {
	if tx == nil || s == nil || s.Clock == nil {
		return errors.New("audit: transaction and clock are required")
	}
	before, beforeJSON, err := snapshot(e.BeforeData)
	if err != nil {
		return err
	}
	after, afterJSON, err := snapshot(e.AfterData)
	if err != nil {
		return err
	}
	if err := normalizeEvent(&e); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// A snapshot established before waiting on the chain lock could miss a
	// committed predecessor in REPEATABLE READ. Require READ COMMITTED rather
	// than allowing such a transaction to silently fork the chain.
	var isolation string
	if err := tx.QueryRow(ctx, `SHOW transaction_isolation`).Scan(&isolation); err != nil {
		return err
	}
	if isolation != "read committed" {
		return errors.New("audit: append requires READ COMMITTED transaction isolation")
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, auditLock); err != nil {
		return err
	}
	previous, err := scanRecord(tx.QueryRow(ctx, `SELECT `+recordColumns+` FROM admin_audit_logs ORDER BY id DESC LIMIT 1`))
	prevHash := zeroHash()
	if err == nil {
		if !validRecord(previous) {
			return ErrChain
		}
		if ok, e := predecessorLink(ctx, tx, previous); e != nil {
			return e
		} else if !ok {
			return ErrChain
		}
		prevHash = previous.RowHash
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var id int64
	if err := tx.QueryRow(ctx, `SELECT nextval(pg_get_serial_sequence('admin_audit_logs','id'))`).Scan(&id); err != nil {
		return err
	}
	if id <= previous.ID || id <= 0 {
		return errors.New("audit: non-monotonic sequence")
	}
	now := s.Clock.Now().UTC().Truncate(time.Microsecond)
	if now.IsZero() || now.Year() < 1 || now.Year() > 9999 {
		return errors.New("audit: invalid business time")
	}
	r := record{Event: e, ID: id, PrevHash: prevHash, CreatedAt: now}
	rowHash, err := hashRecord(r, before, after)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO admin_audit_logs
	 (id,admin_user_id,action_code,target_type,target_id,request_id,ip,user_agent,before_data,after_data,result,step_up_verified,approval_id,prev_hash,row_hash,created_at)
	 OVERRIDING SYSTEM VALUE VALUES($1,$2,$3,NULLIF($4,''),$5,$6,NULLIF($7,'')::inet,NULLIF($8,''),$9,$10,$11,$12,$13,$14,$15,$16)`,
		id, e.AdminUserID, e.ActionCode, e.TargetType, e.TargetID, e.RequestID, e.IP, e.UserAgent, beforeJSON, afterJSON, e.Result, e.StepUpVerified, e.ApprovalID, prevHash, rowHash, now)
	return err
}

func normalizeEvent(e *Event) error {
	if !codePattern.MatchString(e.ActionCode) || (e.TargetType != "" && (len(e.TargetType) > 32 || !codePattern.MatchString(e.TargetType))) || e.RequestID == "" || !plainText(e.RequestID, 32) || !plainText(e.UserAgent, 255) || !validResult(e.Result) {
		return ErrInvalidEvent
	}
	for _, id := range []*int64{e.AdminUserID, e.TargetID, e.ApprovalID} {
		if id != nil && *id <= 0 {
			return ErrInvalidEvent
		}
	}
	if e.IP != "" {
		ip, err := netip.ParseAddr(e.IP)
		if err != nil || ip.Zone() != "" {
			return ErrInvalidEvent
		}
		e.IP = ip.Unmap().String()
	}
	return nil
}

func validResult(s string) bool { return s == "SUCCESS" || s == "FAILED" || s == "DENIED" }

func hashRecord(r record, before, after map[string]any) (string, error) {
	data, err := jcs(map[string]any{
		"id": strconv.FormatInt(r.ID, 10), "admin_user_id": pointerID(r.AdminUserID),
		"action_code": r.ActionCode, "target_type": nullableString(r.TargetType), "target_id": pointerID(r.TargetID),
		"request_id": r.RequestID, "ip": nullableString(r.IP), "user_agent": nullableString(r.UserAgent),
		"before_data": before, "after_data": after, "result": r.Result, "step_up_verified": r.StepUpVerified,
		"approval_id": pointerID(r.ApprovalID), "prev_hash": r.PrevHash, "created_at": r.CreatedAt.UTC().Format(timestampLayout),
	})
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte(r.PrevHash))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil)), nil
}

type rowScanner interface{ Scan(...any) error }

func scanRecord(row rowScanner) (record, error) {
	var r record
	var target, ip, agent *string
	err := row.Scan(&r.ID, &r.AdminUserID, &r.ActionCode, &target, &r.TargetID, &r.RequestID, &ip, &agent, &r.BeforeData, &r.AfterData, &r.Result, &r.StepUpVerified, &r.ApprovalID, &r.PrevHash, &r.RowHash, &r.CreatedAt)
	for dst, source := range map[*string]*string{&r.TargetType: target, &r.IP: ip, &r.UserAgent: agent} {
		if source != nil {
			*dst = *source
			if *source == "" {
				r.invalidNullRepresentation = true
			}
		}
	}
	return r, err
}

func validRecord(r record) bool {
	if r.invalidNullRepresentation || r.ID <= 0 || !hashPattern.MatchString(r.PrevHash) || !hashPattern.MatchString(r.RowHash) || r.CreatedAt.IsZero() || r.CreatedAt.Year() < 1 || r.CreatedAt.Year() > 9999 || r.CreatedAt.Nanosecond()%1000 != 0 {
		return false
	}
	// Read values must already equal their canonical storage representation.
	e := r.Event
	if normalizeEvent(&e) != nil || e.IP != r.IP {
		return false
	}
	before, _, err := snapshot(r.BeforeData)
	if err != nil {
		return false
	}
	after, _, err := snapshot(r.AfterData)
	if err != nil {
		return false
	}
	hash, err := hashRecord(r, before, after)
	return err == nil && hash == r.RowHash
}

func (s *Service) List(ctx context.Context, page, pageSize int, action, result string) (ListResult, error) {
	if page == 0 {
		page = 1
	}
	if pageSize == 0 {
		pageSize = 20
	}
	out := ListResult{Items: []Entry{}, Page: page, PageSize: pageSize, Total: "0"}
	if page < 1 || page > 1000000 || pageSize < 1 || pageSize > 50 || (action != "" && !codePattern.MatchString(action)) || (result != "" && !validResult(result)) {
		return out, httpx.NewError(httpx.ErrBadRequest, "审计列表参数无效")
	}
	if s == nil || s.Pool == nil {
		return out, errors.New("audit: database is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer rollbackRead(tx)
	var count int64
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM admin_audit_logs WHERE ($1='' OR action_code=$1) AND ($2='' OR result=$2)`, action, result).Scan(&count); err != nil {
		return out, err
	}
	out.Total = strconv.FormatInt(count, 10)
	// Qualify the BIGINT column: bare ORDER BY id would choose the id::text
	// output alias and sort 9 before 16 instead of following chain order.
	rows, err := tx.Query(ctx, `SELECT id::text,admin_user_id::text,action_code,target_type,target_id::text,request_id,result,step_up_verified,approval_id::text,prev_hash,row_hash,created_at FROM admin_audit_logs WHERE ($1='' OR action_code=$1) AND ($2='' OR result=$2) ORDER BY admin_audit_logs.id DESC LIMIT $3 OFFSET $4`, action, result, pageSize, int64(page-1)*int64(pageSize))
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var e Entry
		var created time.Time
		if err = rows.Scan(&e.ID, &e.AdminUserID, &e.ActionCode, &e.TargetType, &e.TargetID, &e.RequestID, &e.Result, &e.StepUpVerified, &e.ApprovalID, &e.PrevHash, &e.RowHash, &created); err != nil {
			return out, err
		}
		e.CreatedAt = created.UTC().Format(timestampLayout)
		out.Items = append(out.Items, e)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	rows.Close()
	return out, tx.Commit(ctx)
}

func (s *Service) Verify(ctx context.Context, fromID, toID int64) (VerifyResult, error) {
	out := VerifyResult{FromID: strconv.FormatInt(fromID, 10), ToID: strconv.FormatInt(toID, 10), HeadID: "0", HeadHash: zeroHash()}
	if fromID <= 0 || toID < fromID {
		return out, httpx.NewError(httpx.ErrBadRequest, "校验范围必须为正整数且起点不大于终点")
	}
	if s == nil || s.Pool == nil {
		return out, errors.New("audit: database is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer rollbackRead(tx)
	var head int64
	err = tx.QueryRow(ctx, `SELECT id,row_hash FROM admin_audit_logs ORDER BY id DESC LIMIT 1`).Scan(&head, &out.HeadHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return broken(out, fromID, "MISSING_FROM"), nil
	}
	if err != nil {
		return out, err
	}
	out.HeadID = strconv.FormatInt(head, 10)
	// Limit by actual row count, not ID distance: PostgreSQL sequences consume
	// IDs on rollback, so gaps alone cannot be evidence of a deleted audit row.
	rows, err := tx.Query(ctx, `SELECT `+recordColumns+` FROM admin_audit_logs WHERE id >= $1 AND id <= $2 ORDER BY id LIMIT $3`, fromID, toID, maxVerifyRows+1)
	if err != nil {
		return out, err
	}
	var records []record
	for rows.Next() {
		r, e := scanRecord(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		records = append(records, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(records) > maxVerifyRows {
		return out, httpx.NewError(httpx.ErrBadRequest, "单次最多校验10000条审计记录")
	}
	if len(records) == 0 || records[0].ID != fromID {
		return broken(out, fromID, "MISSING_FROM"), nil
	}
	missingTo := records[len(records)-1].ID != toID
	prevHash := zeroHash()
	predecessor, err := scanRecord(tx.QueryRow(ctx, `SELECT `+recordColumns+` FROM admin_audit_logs WHERE id < $1 ORDER BY id DESC LIMIT 1`, fromID))
	if err == nil {
		if !validRecord(predecessor) {
			return broken(out, predecessor.ID, "PREDECESSOR_HASH_MISMATCH"), nil
		}
		if ok, e := predecessorLink(ctx, tx, predecessor); e != nil {
			return out, e
		} else if !ok {
			return broken(out, predecessor.ID, "PREDECESSOR_LINK_MISMATCH"), nil
		}
		prevHash = predecessor.RowHash
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	for _, r := range records {
		if r.PrevHash != prevHash {
			return broken(out, r.ID, "PREVIOUS_HASH_MISMATCH"), nil
		}
		if !validRecord(r) {
			return broken(out, r.ID, "ROW_HASH_MISMATCH"), nil
		}
		out.Checked++
		prevHash = r.RowHash
	}
	if missingTo {
		return broken(out, toID, "MISSING_TO"), nil
	}
	if err = tx.Commit(ctx); err != nil {
		return out, fmt.Errorf("audit verify snapshot: %w", err)
	}
	out.Valid = true
	return out, nil
}

func broken(out VerifyResult, id int64, reason string) VerifyResult {
	x := strconv.FormatInt(id, 10)
	out.FirstBrokenID = &x
	out.Reason = reason
	return out
}

func predecessorLink(ctx context.Context, tx pgx.Tx, r record) (bool, error) {
	previousHash := zeroHash()
	err := tx.QueryRow(ctx, `SELECT row_hash FROM admin_audit_logs WHERE id<$1 ORDER BY id DESC LIMIT 1`, r.ID).Scan(&previousHash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	return previousHash == r.PrevHash, nil
}

func rollbackRead(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
