package deposit

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/tron"
)

const (
	scanLookback = 48 * time.Hour
	scanOverlap  = 3 * time.Minute
	scanPageCap  = 20
)

// WatchedTransfer 是扫描器眼里的一笔 USDT 转账。Confirmed 为 false 或尚未固化时不得入库。
type WatchedTransfer struct {
	TxHash      string
	EventIndex  int
	Contract    string
	From        string
	To          string
	AmountMicro int64
	BlockNumber int64
	BlockTime   time.Time
	Confirmed   bool
}

// ChainSource 是 TronGrid（或测试替身）。限流和 5xx 必须返回错误，扫描器不会推进游标。
type ChainSource interface {
	SolidifiedHead(ctx context.Context) (int64, time.Time, error)
	IncomingUSDT(ctx context.Context, address string, since time.Time, fingerprint string) ([]WatchedTransfer, string, error)
}

// ScanUSDT 扫描收款地址上的官方 USDT TRC20 转账，写入 chain_transfers 并立即结算。
// 同一笔交易哈希最多入账一次。金额对不上的转账交给结算写成异常，供后台查看。
func (s *Service) ScanUSDT(ctx context.Context, src ChainSource, now time.Time) error {
	if src == nil {
		return errors.New("未配置链上数据源")
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, address FROM receive_addresses
		 WHERE network = 'TRON'
		   AND (
		     status IN ('ACTIVE', 'PAUSED')
		     OR EXISTS (
		       SELECT 1 FROM orders o
		        WHERE o.receive_address_id = receive_addresses.id
		          AND o.pay_method = 'USDT_TRC20'
		          AND o.created_at > now() - interval '7 days'
		     )
		   )
		 ORDER BY id`)
	if err != nil {
		return err
	}
	type addr struct {
		id      int64
		address string
	}
	var addrs []addr
	for rows.Next() {
		var a addr
		if err := rows.Scan(&a.id, &a.address); err != nil {
			rows.Close()
			return err
		}
		addrs = append(addrs, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, a := range addrs {
		if err := s.scanAddress(ctx, src, a.id, a.address, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) scanAddress(ctx context.Context, src ChainSource, addrID int64, address string, now time.Time) error {
	headNum, headAt, err := src.SolidifiedHead(ctx)
	if err != nil {
		return err
	}
	since := now.Add(-scanLookback)
	var cursor time.Time
	err = s.Pool.QueryRow(ctx, `SELECT last_block_time FROM chain_scan_cursors WHERE receive_address_id = $1`, addrID).Scan(&cursor)
	if err == nil {
		since = cursor.Add(-scanOverlap)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}

	var last time.Time
	var lastBlock int64
	advanced := false
	stopped := false
	seen := map[string]struct{}{}
	fingerprint := ""
	for page := 0; page < scanPageCap; page++ {
		if fingerprint != "" {
			if _, ok := seen[fingerprint]; ok {
				break
			}
			seen[fingerprint] = struct{}{}
		}
		items, next, err := src.IncomingUSDT(ctx, address, since, fingerprint)
		if err != nil {
			return err
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].BlockTime.Equal(items[j].BlockTime) {
				if items[i].TxHash == items[j].TxHash {
					return items[i].EventIndex < items[j].EventIndex
				}
				return items[i].TxHash < items[j].TxHash
			}
			return items[i].BlockTime.Before(items[j].BlockTime)
		})
		for _, tr := range items {
			if !tr.Confirmed || !transferSolidified(tr, headNum, headAt) {
				stopped = true
				break
			}
			last, lastBlock, advanced = tr.BlockTime, tr.BlockNumber, true
			if !acceptTransfer(tr, address) {
				if tr.Contract != "" && tr.Contract != tron.OfficialUSDT {
					slog.Info("忽略非官方 USDT 转账", "tx", tr.TxHash)
				}
				continue
			}
			id, needsSettle, err := s.storeTransfer(ctx, addrID, address, tr)
			if err != nil {
				return err
			}
			if needsSettle {
				if _, err := s.Settle(ctx, id); err != nil {
					return err
				}
			}
		}
		if stopped || next == "" {
			break
		}
		fingerprint = next
	}
	if stopped {
		if !advanced {
			return nil
		}
		return s.saveCursor(ctx, addrID, last, lastBlock)
	}
	mark, markBlock := headAt, headNum
	if advanced && last.After(headAt) {
		mark, markBlock = last, lastBlock
	}
	return s.saveCursor(ctx, addrID, mark, markBlock)
}

func transferSolidified(tr WatchedTransfer, headNum int64, headAt time.Time) bool {
	if headAt.IsZero() {
		return false
	}
	if tr.BlockNumber > 0 && headNum > 0 {
		return tr.BlockNumber <= headNum
	}
	return !tr.BlockTime.After(headAt)
}

func acceptTransfer(tr WatchedTransfer, address string) bool {
	return tr.Contract == tron.OfficialUSDT && tr.To == address && tr.From != "" && tr.TxHash != "" && tr.AmountMicro >= minDepositMicro
}

func (s *Service) storeTransfer(ctx context.Context, addrID int64, address string, tr WatchedTransfer) (int64, bool, error) {
	block := tr.BlockNumber
	if block < 0 {
		block = 0
	}
	var id int64
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO chain_transfers (network, tx_hash, event_index, contract_address, from_address, to_address, receive_address_id,
		                             amount_micro, block_number, block_time, confirm_status, data_source, raw)
		VALUES ('TRON', $1, $2, $3, $4, $5, $6, $7, $8, $9, 'SOLIDIFIED', 'TRONGRID', '{"source":"trongrid"}')
		ON CONFLICT (network, tx_hash, event_index) DO NOTHING
		RETURNING id`,
		tr.TxHash, tr.EventIndex, tron.OfficialUSDT, tr.From, address, addrID, tr.AmountMicro, block, tr.BlockTime).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, err
	}
	var status string
	if err := s.Pool.QueryRow(ctx, `
		SELECT id, match_status FROM chain_transfers
		 WHERE network = 'TRON' AND tx_hash = $1 AND event_index = $2`, tr.TxHash, tr.EventIndex).Scan(&id, &status); err != nil {
		return 0, false, err
	}
	return id, status == "UNPROCESSED", nil
}

func (s *Service) saveCursor(ctx context.Context, addrID int64, at time.Time, block int64) error {
	if at.IsZero() {
		return nil
	}
	var blockArg *int64
	if block > 0 {
		blockArg = &block
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO chain_scan_cursors (receive_address_id, last_block_time, last_block_number, last_success_at, updated_at)
		VALUES ($1, $2, $3, now(), now())
		ON CONFLICT (receive_address_id) DO UPDATE
		   SET last_block_time = GREATEST(chain_scan_cursors.last_block_time, EXCLUDED.last_block_time),
		       last_block_number = CASE
		         WHEN EXCLUDED.last_block_time >= chain_scan_cursors.last_block_time THEN EXCLUDED.last_block_number
		         ELSE chain_scan_cursors.last_block_number END,
		       last_success_at = now(),
		       updated_at = now()`, addrID, at, blockArg)
	return err
}
