package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/money"
)

// ExportJobs 由 worker 定时调用：生成待办的导出，并按保留期清理已经到期的。
//
// 生成放在 worker 而不是请求线程里，理由不只是「导出慢」：导出会扫大范围的订单与流量表，
// 放在 admin-api 的请求里，一次范围选错就能把后台的连接池占满，
// 而后台是出事时唯一能用来止血的入口。
type ExportJobs struct {
	Pool  *pgxpool.Pool
	Clock clock.Clock
}

// RunExports 生成待办导出。每次最多处理 limit 个，每个各自一笔事务：
// 一个导出失败不该连累排在它后面的。
func (j *ExportJobs) RunExports(ctx context.Context, now time.Time) error {
	if j == nil || j.Pool == nil {
		return errors.New("export jobs require a pool")
	}
	const limit = 4
	for i := 0; i < limit; i++ {
		done, err := j.runOne(ctx, now)
		if err != nil {
			return err
		}
		if !done {
			return nil
		}
	}
	return nil
}

// runOne 取一条 PENDING 生成完。返回 false 表示队列已空。
//
// 取件用 FOR UPDATE SKIP LOCKED：多个 worker 实例并存时，谁也不会去抢已经有人在做的那条，
// 也不会因为前一条正在生成而整体卡住。
func (j *ExportJobs) runOne(ctx context.Context, now time.Time) (bool, error) {
	tx, err := j.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return false, err
	}
	defer rollbackApproval(tx)

	var id int64
	var kind, watermark string
	var from, to *time.Time
	err = tx.QueryRow(ctx, `SELECT id,kind,watermark,range_from,range_to FROM admin_exports
 WHERE status='PENDING' ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1`).
		Scan(&id, &kind, &watermark, &from, &to)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	content, rows, genErr := buildExport(ctx, tx, id, kind, watermark, from, to, now)
	if genErr != nil {
		// 业务性失败（超限）记进 failure_reason 让运维看得见；
		// 基础设施失败不该被写成「导出失败」——那会让人以为是数据的问题，
		// 所以原样抛出去，任务保持 PENDING，下一轮重试。
		var over *exportTooLarge
		if !errors.As(genErr, &over) {
			return false, genErr
		}
		if _, err = tx.Exec(ctx, `UPDATE admin_exports SET status='FAILED',failure_reason=$2,
 finished_at=$3 WHERE id=$1 AND status='PENDING'`, id, over.Error(), now); err != nil {
			return false, err
		}
		return true, tx.Commit(ctx)
	}

	sum := sha256.Sum256(content)
	if _, err = tx.Exec(ctx, `UPDATE admin_exports SET status='READY',content=$2,
 content_sha256=$3,row_count=$4,byte_size=$5,finished_at=$6,expires_at=$7
 WHERE id=$1 AND status='PENDING'`,
		id, content, hex.EncodeToString(sum[:]), rows, len(content), now, now.Add(exportTTL)); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// exportTooLarge 是唯一一种「数据本身的问题」，与基础设施故障区分开。
type exportTooLarge struct{ msg string }

func (e *exportTooLarge) Error() string { return e.msg }

// buildExport 生成 CSV。
//
// 首行与末行是以 # 开头的水印行（方案 12.4 要求水印含管理员账号与时间）。
// 末行重复一次水印，是为了让消费方能判断文件有没有被截断——只看首行做不到这件事。
// 带 UTF-8 BOM：没有 BOM 时 Excel 会把中文列名读成乱码，而这份文件的第一个消费者就是人。
func buildExport(ctx context.Context, tx pgx.Tx, id int64, kind, watermark string,
	from, to *time.Time, now time.Time) ([]byte, int64, error) {
	var buf bytes.Buffer
	buf.WriteString("\xef\xbb\xbf")
	head := fmt.Sprintf("# %s | export_id=%d | kind=%s | range=%s | generated_at=%s",
		watermark, id, kind, rangeLabel(from, to), now.UTC().Format(time.RFC3339))
	buf.WriteString(head + "\n")

	w := csv.NewWriter(&buf)
	header, next, closeRows, err := exportRows(ctx, tx, kind, from, to, now)
	if err != nil {
		return nil, 0, err
	}
	// 必须无条件关闭。撞上限时是中途返回的，游标还开着；而调用方紧接着要在**同一笔事务**
	// 里把任务置为 FAILED——游标没关，那条 UPDATE 会直接撞上 "conn busy"，
	// 于是「超限」这条本该被记下来的失败，反而变成一个基础设施错误被抛掉。
	defer closeRows()
	if err = w.Write(header); err != nil {
		return nil, 0, err
	}
	var count int64
	for {
		rec, ok, err := next()
		if err != nil {
			return nil, 0, err
		}
		if !ok {
			break
		}
		if err = w.Write(rec); err != nil {
			return nil, 0, err
		}
		count++
		if count > exportMaxRows {
			return nil, 0, &exportTooLarge{msg: fmt.Sprintf(
				"导出超过 %d 行上限，请缩小区间后分批导出", exportMaxRows)}
		}
		// 每 2000 行冲一次并看一眼体积：全部写完再判断，意味着超限时内存里
		// 已经躺着一份超大的缓冲区了。
		if count%2000 == 0 {
			w.Flush()
			if err = w.Error(); err != nil {
				return nil, 0, err
			}
			if buf.Len() > exportMaxBytes {
				return nil, 0, &exportTooLarge{msg: fmt.Sprintf(
					"导出超过 %d MiB 上限，请缩小区间后分批导出", exportMaxBytes>>20)}
			}
		}
	}
	w.Flush()
	if err = w.Error(); err != nil {
		return nil, 0, err
	}
	if buf.Len() > exportMaxBytes {
		return nil, 0, &exportTooLarge{msg: fmt.Sprintf(
			"导出超过 %d MiB 上限，请缩小区间后分批导出", exportMaxBytes>>20)}
	}
	buf.WriteString(fmt.Sprintf("# end | export_id=%d | rows=%d | %s\n", id, count, watermark))
	return buf.Bytes(), count, nil
}

func rangeLabel(from, to *time.Time) string {
	if from == nil || to == nil {
		return "point-in-time"
	}
	return from.Format("2006-01-02") + ".." + to.Format("2006-01-02")
}

// exportRows 返回表头与一个逐行取数的闭包。
//
// 逐行取而不是一次性 Query 到切片：导出件可以有二十万行，先全部装进内存再序列化
// 等于把上限检查变成马后炮。
func exportRows(ctx context.Context, tx pgx.Tx, kind string, from, to *time.Time, now time.Time) (
	[]string, func() ([]string, bool, error), func(), error) {
	switch kind {
	case "ORDERS":
		return exportOrders(ctx, tx, *from, *to)
	case "CHAIN_TRANSFERS":
		return exportChainTransfers(ctx, tx, *from, *to)
	case "TRAFFIC":
		return exportTraffic(ctx, tx, *from, *to)
	case "RECONCILE":
		return exportReconcile(ctx, tx, now)
	}
	return nil, nil, nil, fmt.Errorf("unknown export kind %q", kind)
}

// dayEnd 把闭区间的右端换算成时间戳上界：to 当天 24:00（UTC）。
// 直接拿 to 当时间戳会把最后一天整天漏掉，而契约写的是闭区间。
func dayEnd(to time.Time) time.Time { return to.AddDate(0, 0, 1) }

// rowIter 返回取数闭包与关闭闭包。关闭是幂等的（pgx 的 Close 可以重复调用），
// 调用方无条件 defer 它即可。
func rowIter(rows pgx.Rows, scan func(pgx.Rows) ([]string, error)) (
	func() ([]string, bool, error), func()) {
	next := func() ([]string, bool, error) {
		if !rows.Next() {
			err := rows.Err()
			rows.Close()
			return nil, false, err
		}
		rec, err := scan(rows)
		if err != nil {
			rows.Close()
			return nil, false, err
		}
		return rec, true, nil
	}
	return next, rows.Close
}

// 三个周期类导出的投影与资金只读接口同一套口径：金额是固定 6 位小数的字符串，
// 链上 raw 载荷、收款地址签名原文、订单 client_idem_key 一律不出现。
func exportOrders(ctx context.Context, tx pgx.Tx, from, to time.Time) (
	[]string, func() ([]string, bool, error), func(), error) {
	rows, err := tx.Query(ctx, `SELECT order_no,user_id::text,order_type,status,pay_method,
 base_amount_micro,pay_amount_micro,is_late_payment,COALESCE(fulfillment_result,''),
 expires_at,paid_at,completed_at,created_at
 FROM orders WHERE created_at>=$1 AND created_at<$2 ORDER BY created_at,id`, from, dayEnd(to))
	if err != nil {
		return nil, nil, nil, err
	}
	header := []string{"order_no", "user_id", "order_type", "status", "pay_method",
		"base_amount", "pay_amount", "currency", "is_late_payment", "fulfillment_result",
		"expires_at", "paid_at", "completed_at", "created_at"}
	next, done := rowIter(rows, func(r pgx.Rows) ([]string, error) {
		var no, uid, typ, status, method, fulfill string
		var base, pay int64
		var late bool
		var expires, created time.Time
		var paid, completed *time.Time
		if err := r.Scan(&no, &uid, &typ, &status, &method, &base, &pay, &late, &fulfill,
			&expires, &paid, &completed, &created); err != nil {
			return nil, err
		}
		return []string{no, uid, typ, status, method,
			money.Micro(base).String(), money.Micro(pay).String(), "USDT",
			strconv.FormatBool(late), fulfill,
			ts(&expires), ts(paid), ts(completed), ts(&created)}, nil
	})
	return header, next, done, nil
}

func exportChainTransfers(ctx context.Context, tx pgx.Tx, from, to time.Time) (
	[]string, func() ([]string, bool, error), func(), error) {
	rows, err := tx.Query(ctx, `SELECT t.id::text,t.network,t.tx_hash,t.from_address,t.to_address,
 t.amount_micro,t.block_number::text,t.block_time,t.confirm_status,t.match_status,
 COALESCE(o.order_no,''),t.created_at
 FROM chain_transfers t LEFT JOIN orders o ON o.id=t.order_id
 WHERE t.block_time>=$1 AND t.block_time<$2 ORDER BY t.block_time,t.id`, from, dayEnd(to))
	if err != nil {
		return nil, nil, nil, err
	}
	header := []string{"id", "network", "tx_hash", "from_address", "to_address",
		"amount", "currency", "block_number", "block_time", "confirm_status",
		"match_status", "order_no", "created_at"}
	next, done := rowIter(rows, func(r pgx.Rows) ([]string, error) {
		var id, network, hash, fromAddr, toAddr, block, confirm, match, orderNo string
		var amount int64
		var blockTime, created time.Time
		if err := r.Scan(&id, &network, &hash, &fromAddr, &toAddr, &amount, &block,
			&blockTime, &confirm, &match, &orderNo, &created); err != nil {
			return nil, err
		}
		return []string{id, network, hash, fromAddr, toAddr,
			money.Micro(amount).String(), "USDT", block, ts(&blockTime),
			confirm, match, orderNo, ts(&created)}, nil
	})
	return header, next, done, nil
}

func exportTraffic(ctx context.Context, tx pgx.Tx, from, to time.Time) (
	[]string, func() ([]string, bool, error), func(), error) {
	// 流量按 stat_date 取，闭区间两端都算。
	rows, err := tx.Query(ctx, `SELECT to_char(stat_date,'YYYY-MM-DD'),user_id::text,device_id::text,
 node_id::text,upload_bytes::text,download_bytes::text,(upload_bytes+download_bytes)::text
 FROM traffic_daily WHERE stat_date>=$1 AND stat_date<=$2
 ORDER BY stat_date,user_id,device_id,node_id`, from, to)
	if err != nil {
		return nil, nil, nil, err
	}
	header := []string{"stat_date", "user_id", "device_id", "node_id",
		"upload_bytes", "download_bytes", "total_bytes"}
	next, done := rowIter(rows, func(r pgx.Rows) ([]string, error) {
		rec := make([]string, 7)
		if err := r.Scan(&rec[0], &rec[1], &rec[2], &rec[3], &rec[4], &rec[5], &rec[6]); err != nil {
			return nil, err
		}
		return rec, nil
	})
	return header, next, done, nil
}

// exportReconcile 导出方案 10.5 的对账异常清单。
//
// 三类异常合成一张表，用 category 区分：一个 CSV 不该逼运维打开三个文件才能看全，
// 而三类各自的标识含义不同，所以第二列叫 subject 而不是 id。
// 口径与 internal/deposit 的 Reconcile 一致——那是这三类异常唯一的定义处；
// 这里重述它的 SQL 而不是调用它，是因为导出必须在**同一笔只读事务的同一个快照**里取数，
// 而 Reconcile 用的是自己的连接池、三条独立查询，快照互不相同。
// 这一点是本节明确的取舍：多一份 SQL 的漂移风险，换导出件内部自洽。
func exportReconcile(ctx context.Context, tx pgx.Tx, now time.Time) (
	[]string, func() ([]string, bool, error), func(), error) {
	rows, err := tx.Query(ctx, `
 SELECT 'BALANCE_MISMATCH', b.user_id::text,
        (b.available_micro - COALESCE(SUM(l.amount_micro),0))::text
   FROM user_balances b LEFT JOIN balance_ledger l ON l.user_id=b.user_id
  GROUP BY b.user_id, b.available_micro
 HAVING b.available_micro <> COALESCE(SUM(l.amount_micro),0)
 UNION ALL
 SELECT 'STUCK_TRANSFER', t.id::text, to_char(t.created_at,'YYYY-MM-DD"T"HH24:MI:SSOF')
   FROM chain_transfers t
  WHERE t.confirm_status='SOLIDIFIED' AND t.match_status='UNPROCESSED'
    AND t.created_at < $1::timestamptz - interval '10 minutes'
 UNION ALL
 SELECT 'ORDER_NO_FULFILL', o.order_no, to_char(o.completed_at,'YYYY-MM-DD"T"HH24:MI:SSOF')
   FROM orders o
  WHERE o.status='COMPLETED' AND o.order_type='PLAN'
    AND NOT EXISTS (SELECT 1 FROM subscription_ledger l WHERE l.idempotency_key='fulfill:order:'||o.id)
    AND NOT EXISTS (SELECT 1 FROM balance_ledger b WHERE b.idempotency_key='fulfill_fallback:order:'||o.id)
 ORDER BY 1,2`, now)
	if err != nil {
		return nil, nil, nil, err
	}
	// detail 的含义随 category 变：余额不符给差额（微单位），另外两类给时间。
	// 与其造三个大半为空的列，不如明说这一列是随类而变的。
	header := []string{"category", "subject", "detail"}
	next, done := rowIter(rows, func(r pgx.Rows) ([]string, error) {
		rec := make([]string, 3)
		if err := r.Scan(&rec[0], &rec[1], &rec[2]); err != nil {
			return nil, err
		}
		return rec, nil
	})
	return header, next, done, nil
}

func ts(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// PurgeExports 执行方案 12.4 的 24 小时保留期：到期的导出件内容清空、状态转 EXPIRED，
// 记录本身再留 7 天以便审计，之后整行删除。
//
// 分两步而不是到期直接删行：审计日志里会引用导出编号，编号立刻消失会让审计链上出现
// 查不到对应物的引用。
func (j *ExportJobs) PurgeExports(ctx context.Context, now time.Time) error {
	if j == nil || j.Pool == nil {
		return errors.New("export jobs require a pool")
	}
	if _, err := j.Pool.Exec(ctx, `UPDATE admin_exports
 SET status='EXPIRED',content=NULL WHERE status='READY' AND expires_at<=$1`, now); err != nil {
		return err
	}
	_, err := j.Pool.Exec(ctx, `DELETE FROM admin_exports
 WHERE status IN ('EXPIRED','FAILED') AND created_at < $1`, now.Add(-7*24*time.Hour))
	return err
}
