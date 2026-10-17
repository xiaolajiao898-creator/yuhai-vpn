package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/httpx"
)

// 数据导出（admin.yaml POST /exports、GET /exports、POST /exports/{id}/download）。
//
// 方案 12.4「敏感信息展示」对导出的三条要求，在这一节里各自有一个落点：
//
//   - **导出行为需 L2** —— 发起走 securedMutation（二次验证 + 幂等 + 审计）；
//     下载单独再要一次二次验证，并且**每下载一次写一条审计**。
//   - **文件带水印（管理员账号 + 时间）** —— 水印是 admin_exports 的 NOT NULL 列，
//     同时逐字出现在 CSV 的首行与末行（末行还能用来判断文件有没有被截断）。
//   - **24 小时后自动删除** —— expires_at 由生成时写死，worker 的 PurgeExports 执行。
//
// 「发起」与「下载」是两个能力。发起只是登记一个任务，**数据真正离开系统是在下载那一刻**，
// 所以可以让人能发起而不能自取，也可以让审计岗只取不发。
type ExportService struct {
	Security *ApprovalService
	// Read 用于列表：列表是 L0 只读，沿用与资金只读同一套会话/权限骨架。
	Read *FinanceReadService
}

const (
	actionExportCreate   = "exports.create"
	actionExportDownload = "exports.download"
	exportRead           = "exports.read"

	// 保留期（方案 12.4）。
	exportTTL = 24 * time.Hour
	// 区间跨度上限。92 天 ≈ 一个季度，够对账和报表用；再长就该分批取。
	exportMaxSpanDays = 92
)

// 生成上限，先到者为准。超限不给半份数据，而是把任务置为 FAILED 并说明要缩小区间。
//
// 是 var 而不是 const，只为一件事：集成测试要真的撞一次上限，而按真实值撞需要先造
// 二十万行。测试把它们临时调小再还原——比起「上限从没被执行过」，这点代价是划算的。
var (
	exportMaxRows  int64 = 200_000
	exportMaxBytes       = 32 << 20
)

var exportKinds = map[string]bool{
	"ORDERS": true, "CHAIN_TRANSFERS": true, "TRAFFIC": true, "RECONCILE": true,
}

// exportRanged 说明这个 kind 是不是按时间区间取数。
// RECONCILE 是「此刻的异常清单」，不是某段时间的流水——给它一个区间只会让人以为筛过了，
// 所以带上 from/to 一律拒绝，而不是静默忽略。
func exportRanged(kind string) bool { return kind != "RECONCILE" }

type ExportCreateInput struct {
	OperationID string `json:"operation_id"`
	Reason      string `json:"reason"`
	Kind        string `json:"kind"`
	From        string `json:"from"`
	To          string `json:"to"`
}

type ExportDownloadInput struct {
	Reason string `json:"reason"`
}

// ManagedExport 是导出任务的元数据投影。**永远不含文件内容**——
// 取文件只有 POST /exports/{id}/download 一条路，那条路要单独的能力和单独的二次验证。
type ManagedExport struct {
	ID            string     `json:"id"`
	Kind          string     `json:"kind"`
	RangeFrom     *string    `json:"range_from"`
	RangeTo       *string    `json:"range_to"`
	Status        string     `json:"status"`
	RequestedBy   string     `json:"requested_by"`
	Watermark     string     `json:"watermark"`
	RowCount      *string    `json:"row_count"`
	ByteSize      *string    `json:"byte_size"`
	ContentSHA256 *string    `json:"content_sha256"`
	FailureReason *string    `json:"failure_reason"`
	CreatedAt     time.Time  `json:"created_at"`
	FinishedAt    *time.Time `json:"finished_at"`
	ExpiresAt     *time.Time `json:"expires_at"`
}

type ManagedExportPage struct {
	Items      []ManagedExport `json:"items"`
	Total      string          `json:"total"`
	Page       int             `json:"page"`
	PageSize   int             `json:"page_size"`
	ObservedAt time.Time       `json:"observed_at"`
}

const exportColumns = `e.id::text,e.kind,to_char(e.range_from,'YYYY-MM-DD'),to_char(e.range_to,'YYYY-MM-DD'),
 e.status,u.username,e.watermark,e.row_count::text,e.byte_size::text,e.content_sha256,
 e.failure_reason,e.created_at,e.finished_at,e.expires_at`

func scanExport(row pgx.Row) (ManagedExport, error) {
	var e ManagedExport
	err := row.Scan(&e.ID, &e.Kind, &e.RangeFrom, &e.RangeTo, &e.Status, &e.RequestedBy,
		&e.Watermark, &e.RowCount, &e.ByteSize, &e.ContentSHA256, &e.FailureReason,
		&e.CreatedAt, &e.FinishedAt, &e.ExpiresAt)
	return e, err
}

// RequestExport 登记一次导出。返回 202 时**文件还不存在**：生成是 worker 的事。
//
// 这里刻意不预检行数。预检要先把目标区间整个扫一遍 count(*)，代价比「生成到一半发现超限、
// 把任务置为 FAILED 并告诉运维缩小区间」大得多，而且预检与生成之间数据还会继续增长，
// 预检通过也不代表生成一定不超限——那就成了一个既贵又不作数的检查。
func (s *ExportService) RequestExport(ctx context.Context, p ApprovalProof, in ExportCreateInput) (
	out ManagedExport, err error) {
	if s == nil || s.Security == nil {
		return out, httpx.Internal
	}
	if !exportKinds[in.Kind] {
		return out, httpx.NewError(httpx.ErrBadRequest, "导出类型只能是 ORDERS、CHAIN_TRANSFERS、TRAFFIC 或 RECONCILE")
	}
	from, to, err := exportRange(in.Kind, in.From, in.To)
	if err != nil {
		return out, err
	}

	raw, err := securedMutation(ctx, s.Security, "admin_export_mutations", "admin-export",
		p, actionExportCreate, "export", 1, in.OperationID, in.Reason,
		func(c context.Context, tx pgx.Tx, actor int64, now time.Time) (int64, any, json.RawMessage, json.RawMessage, error) {
			var username string
			if e := tx.QueryRow(c, `SELECT username FROM admin_users WHERE id=$1`, actor).Scan(&username); e != nil {
				return 0, nil, nil, nil, e
			}
			// 水印按方案 12.4 = 管理员账号 + 时间。发起时就定死，生成时不再重算——
			// 水印要认的是「谁在什么时候要了这份数据」，不是「机器什么时候跑完的」。
			watermark := fmt.Sprintf("yuhai-vpn export | requested_by=%s | requested_at=%s",
				username, now.UTC().Format(time.RFC3339))
			var id int64
			e := tx.QueryRow(c, `INSERT INTO admin_exports(kind,range_from,range_to,requested_by,
 watermark,status,created_at) VALUES($1,$2,$3,$4,$5,'PENDING',$6) RETURNING id`,
				in.Kind, from, to, actor, watermark, now).Scan(&id)
			if e != nil {
				return 0, nil, nil, nil, e
			}
			entry, e := scanExport(tx.QueryRow(c, `SELECT `+exportColumns+
				` FROM admin_exports e JOIN admin_users u ON u.id=e.requested_by WHERE e.id=$1`, id))
			if e != nil {
				return 0, nil, nil, nil, e
			}
			after, _ := json.Marshal(map[string]any{"target_type": "export",
				"target_id": entry.ID, "status": entry.Status, "code": entry.Kind,
				"decision_note": in.Reason})
			return id, entry, nil, after, nil
		})
	if err != nil {
		return ManagedExport{}, err
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return ManagedExport{}, httpx.Internal
	}
	return out, nil
}

// exportRange 校验区间。区间类必填、闭区间、跨度有上限；RECONCILE 必须不填。
// 返回的是 *time.Time，nil 表示这个 kind 没有区间。
func exportRange(kind, from, to string) (*time.Time, *time.Time, error) {
	if !exportRanged(kind) {
		if from != "" || to != "" {
			return nil, nil, httpx.NewError(httpx.ErrBadRequest,
				"RECONCILE 导出的是此刻的对账异常清单，不接受 from/to")
		}
		return nil, nil, nil
	}
	if from == "" || to == "" {
		return nil, nil, httpx.NewError(httpx.ErrBadRequest, "该导出类型必须给出 from 与 to（YYYY-MM-DD）")
	}
	f, e := time.Parse("2006-01-02", from)
	if e != nil {
		return nil, nil, httpx.NewError(httpx.ErrBadRequest, "from 必须是 YYYY-MM-DD")
	}
	t, e := time.Parse("2006-01-02", to)
	if e != nil {
		return nil, nil, httpx.NewError(httpx.ErrBadRequest, "to 必须是 YYYY-MM-DD")
	}
	if t.Before(f) {
		return nil, nil, httpx.NewError(httpx.ErrBadRequest, "to 不能早于 from")
	}
	// 闭区间，两端都算在内，所以跨度是差值 +1 天。
	if t.Sub(f) >= exportMaxSpanDays*24*time.Hour {
		return nil, nil, httpx.NewError(httpx.ErrBadRequest,
			"导出区间不能超过 "+strconv.Itoa(exportMaxSpanDays)+" 天，请分批导出")
	}
	return &f, &t, nil
}

type ExportFilter struct {
	Page, PageSize int
	Kind, Status   string
}

var exportStatuses = map[string]bool{"PENDING": true, "READY": true, "FAILED": true, "EXPIRED": true}

// Exports 列出导出任务。只读，永远不返回文件内容。
func (s *ExportService) Exports(ctx context.Context, p Principal, f ExportFilter) (
	out ManagedExportPage, err error) {
	if s == nil || s.Read == nil {
		return out, httpx.Internal
	}
	page, size, err := normalizeUserPage(f.Page, f.PageSize)
	if err != nil {
		return out, err
	}
	if f.Kind != "" && !exportKinds[f.Kind] {
		return out, httpx.NewError(httpx.ErrBadRequest, "导出类型无效")
	}
	if f.Status != "" && !exportStatuses[f.Status] {
		return out, httpx.NewError(httpx.ErrBadRequest, "导出状态无效")
	}
	err = s.Read.readWith(ctx, p, exportRead, func(c context.Context, tx pgx.Tx, now time.Time) error {
		const where = ` FROM admin_exports e JOIN admin_users u ON u.id=e.requested_by
 WHERE ($1='' OR e.kind=$1) AND ($2='' OR e.status=$2)`
		var total int64
		if e := tx.QueryRow(c, `SELECT count(*)`+where, f.Kind, f.Status).Scan(&total); e != nil {
			return e
		}
		rows, e := tx.Query(c, `SELECT `+exportColumns+where+
			` ORDER BY e.created_at DESC,e.id DESC LIMIT $3 OFFSET $4`,
			f.Kind, f.Status, size, (page-1)*size)
		if e != nil {
			return e
		}
		defer rows.Close()
		out = ManagedExportPage{Items: []ManagedExport{}, Total: strconv.FormatInt(total, 10),
			Page: page, PageSize: size, ObservedAt: now}
		for rows.Next() {
			v, e := scanExport(rows)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, v)
		}
		return rows.Err()
	})
	if err != nil {
		return ManagedExportPage{}, err
	}
	return out, nil
}

// ExportFile 是一次成功下载的结果。
type ExportFile struct {
	Name    string
	SHA256  string
	Content []byte
}

// Download 取回导出文件。
//
// **这里刻意没有走 securedMutation。** 那套骨架的核心是 operation_id 幂等：
// 同一操作重复提交返回逐字节相同的首次响应。下载没有这个语义——重复下载同一份文件
// 本来就允许，而把 CSV 再存一份进幂等表只是白白多一份副本。所以这里只保留
// securedMutation 里对下载真正成立的那几件事：二次验证（令牌一次一用）、
// 权限锁定、以及**每次下载各写一条审计**。数据离开系统的次数必须逐次可数。
func (s *ExportService) Download(ctx context.Context, p ApprovalProof, exportID int64,
	in ExportDownloadInput) (out ExportFile, err error) {
	a := s.securityOrNil()
	if a == nil {
		return out, httpx.Internal
	}
	if exportID <= 0 {
		return out, httpx.NewError(httpx.ErrBadRequest, "导出编号无效")
	}
	event := a.auditEvent(p, actionExportDownload, "export", exportID)
	event.UserAgent = ""
	defer func() {
		if err != nil {
			err = a.RecordFailure(ctx, event, err)
		}
	}()
	if !approvalText(in.Reason) {
		err = httpx.NewError(httpx.ErrBadRequest, "下载理由无效")
		return out, err
	}
	if p.Token == "" {
		err = httpx.NewError(httpx.ErrStepUpRequired, "请先完成二次验证")
		return out, err
	}
	if !validToken(p.Token) || !stepUpHash.MatchString(p.RequestHash) {
		err = errStepUp
		return out, err
	}
	c, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	tx, e := a.Auth.Pool.BeginTx(c, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if e != nil {
		err = e
		return out, err
	}
	defer rollbackApproval(tx)
	actor, e := a.Auth.lockStepUpAuthority(c, tx, p.Principal, actionExportDownload, p.IP)
	if e != nil {
		err = e
		return out, err
	}
	if e = lockApprovalPermission(c, tx, actor.id, actionExportDownload, 2, a.Auth.Clock.Now()); e != nil {
		err = e
		return out, err
	}
	if e = a.Auth.ConsumeStepUp(c, tx, p.Principal, p.Token, actionExportDownload, p.RequestHash, p.IP); e != nil {
		err = e
		return out, err
	}
	event.StepUpVerified = true

	now := a.Auth.Clock.Now()
	var kind, status string
	var sum *string
	var reason *string
	var expires *time.Time
	var content []byte
	e = tx.QueryRow(c, `SELECT kind,status,content_sha256,failure_reason,expires_at,content
 FROM admin_exports WHERE id=$1`, exportID).
		Scan(&kind, &status, &sum, &reason, &expires, &content)
	if errors.Is(e, pgx.ErrNoRows) {
		err = httpx.NewError(httpx.ErrNotFound, "导出不存在")
		return out, err
	}
	if e != nil {
		err = e
		return out, err
	}
	switch {
	case status == "PENDING":
		err = httpx.NewError(httpx.ErrConflict, "该导出还在生成中，请稍后再试")
		return out, err
	case status == "FAILED":
		msg := "该导出生成失败"
		if reason != nil {
			msg += "：" + *reason
		}
		err = httpx.NewError(httpx.ErrConflict, msg)
		return out, err
	// 过了保留期就是过了，不会临时再生成一份。这里不看 status 而看时间：
	// 清理任务是每分钟跑一次的，到期与被清掉之间总有一个窗口，
	// 只认 status 会让文件在这个窗口里继续可下载。
	case status == "EXPIRED" || expires == nil || !now.Before(*expires) || content == nil:
		err = httpx.NewError(httpx.ErrNotFound, "该导出已过 24 小时保留期，文件已删除")
		return out, err
	}

	event.BeforeData, _ = json.Marshal(map[string]any{"target_type": "export",
		"target_id": strconv.FormatInt(exportID, 10), "status": status, "code": kind})
	event.AfterData, _ = json.Marshal(map[string]any{"target_type": "export",
		"target_id": strconv.FormatInt(exportID, 10), "status": status, "code": kind,
		"decision_note": in.Reason})
	if e = a.Audit.Append(c, tx, event); e != nil {
		err = e
		return out, err
	}
	if e = a.freshProof(c, tx, p); e != nil {
		err = e
		return out, err
	}
	if e = tx.Commit(c); e != nil {
		err = e
		return out, err
	}
	file := ExportFile{
		Name:    fmt.Sprintf("yuhai-%s-%d.csv", kindSlug(kind), exportID),
		Content: content,
	}
	if sum != nil {
		file.SHA256 = *sum
	}
	return file, nil
}

func (s *ExportService) securityOrNil() *ApprovalService {
	if s == nil || s.Security == nil || s.Security.Auth == nil || s.Security.Auth.Pool == nil ||
		s.Security.Auth.Clock == nil || s.Security.Audit == nil {
		return nil
	}
	return s.Security
}

func kindSlug(kind string) string {
	switch kind {
	case "ORDERS":
		return "orders"
	case "CHAIN_TRANSFERS":
		return "chain-transfers"
	case "TRAFFIC":
		return "traffic"
	default:
		return "reconcile"
	}
}
