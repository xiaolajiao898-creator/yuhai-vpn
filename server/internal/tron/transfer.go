package tron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrTransferNotFound 表示链上还没有符合条件的已确认转账。
var ErrTransferNotFound = errors.New("未找到符合条件的转账")

// Transfer 是一笔已确认的 TRX 转账。
type Transfer struct {
	TxID      string
	AmountSun int64
	BlockTime time.Time
}

// TransferFinder 在链上查找「from → to、金额恰好为 amountSun、时间不早于 since」的已确认 TRX 转账。
type TransferFinder interface {
	FindTRXTransfer(ctx context.Context, from, to string, amountSun int64, since time.Time) (Transfer, error)
}

// TronGrid 通过 TronGrid v1 接口查询。只读，不涉及任何私钥。
type TronGrid struct {
	BaseURL string // 默认 https://api.trongrid.io
	APIKey  string
	HTTP    *http.Client
}

type tgResponse struct {
	Success bool `json:"success"`
	Data    []struct {
		TxID           string `json:"txID"`
		BlockTimestamp int64  `json:"block_timestamp"`
		Ret            []struct {
			ContractRet string `json:"contractRet"`
		} `json:"ret"`
		RawData struct {
			Contract []struct {
				Type      string `json:"type"`
				Parameter struct {
					Value struct {
						Amount       int64  `json:"amount"`
						OwnerAddress string `json:"owner_address"`
						ToAddress    string `json:"to_address"`
					} `json:"value"`
				} `json:"parameter"`
			} `json:"contract"`
		} `json:"raw_data"`
	} `json:"data"`
}

// FindTRXTransfer 实现 TransferFinder。
func (g *TronGrid) FindTRXTransfer(ctx context.Context, from, to string, amountSun int64, since time.Time) (Transfer, error) {
	fromHex, err := HexAddress(from)
	if err != nil {
		return Transfer{}, err
	}
	toHex, err := HexAddress(to)
	if err != nil {
		return Transfer{}, err
	}
	base := strings.TrimRight(g.BaseURL, "/")
	if base == "" {
		base = "https://api.trongrid.io"
	}
	q := url.Values{}
	q.Set("only_from", "true")
	q.Set("only_confirmed", "true")
	q.Set("limit", "50")
	q.Set("min_timestamp", fmt.Sprint(since.UnixMilli()))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/accounts/"+from+"/transactions?"+q.Encode(), nil)
	if err != nil {
		return Transfer{}, err
	}
	req.Header.Set("Accept", "application/json")
	if g.APIKey != "" {
		req.Header.Set("TRON-PRO-API-KEY", g.APIKey)
	}
	client := g.HTTP
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Transfer{}, fmt.Errorf("查询链上记录失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Transfer{}, fmt.Errorf("查询链上记录失败: HTTP %d", resp.StatusCode)
	}
	var body tgResponse
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 4<<20)).Decode(&body); err != nil {
		return Transfer{}, fmt.Errorf("链上记录格式错误: %w", err)
	}
	for _, tx := range body.Data {
		if len(tx.Ret) == 0 || tx.Ret[0].ContractRet != "SUCCESS" || len(tx.RawData.Contract) != 1 {
			continue
		}
		c := tx.RawData.Contract[0]
		v := c.Parameter.Value
		if c.Type != "TransferContract" || !strings.EqualFold(v.OwnerAddress, fromHex) || !strings.EqualFold(v.ToAddress, toHex) {
			continue
		}
		bt := time.UnixMilli(tx.BlockTimestamp)
		if v.Amount != amountSun || bt.Before(since) || tx.TxID == "" {
			continue
		}
		return Transfer{TxID: strings.ToLower(tx.TxID), AmountSun: v.Amount, BlockTime: bt}, nil
	}
	return Transfer{}, ErrTransferNotFound
}
