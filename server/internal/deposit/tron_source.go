package deposit

import (
	"context"
	"time"

	"github.com/yuhai-vpn/server/internal/tron"
)

// TronSource 把 TronGrid 适配成扫描器的数据源。没有 API key 时也能用，只是频率更低。
type TronSource struct {
	Grid *tron.TronGrid
}

func (s TronSource) SolidifiedHead(ctx context.Context) (int64, time.Time, error) {
	return s.Grid.SolidifiedHead(ctx)
}

func (s TronSource) IncomingUSDT(ctx context.Context, address string, since time.Time, fingerprint string) ([]WatchedTransfer, string, error) {
	rows, next, err := s.Grid.IncomingUSDT(ctx, address, since, fingerprint)
	if err != nil {
		return nil, "", err
	}
	out := make([]WatchedTransfer, 0, len(rows))
	for _, row := range rows {
		out = append(out, WatchedTransfer{
			TxHash: row.TxHash, EventIndex: row.EventIndex, Contract: row.Contract,
			From: row.From, To: row.To, AmountMicro: row.AmountMicro,
			BlockNumber: row.BlockNumber, BlockTime: row.BlockTime, Confirmed: row.Confirmed,
		})
	}
	return out, next, nil
}
