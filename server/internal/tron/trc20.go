package tron

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// OfficialUSDT 是 TRON 主网 USDT（TRC20）的官方合约。扫描只认这一份。
const OfficialUSDT = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"

// APIError 是 TronGrid 返回的可重试错误。调用方不得据此推进扫描游标。
type APIError struct {
	Op     string
	Status int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("TronGrid %s 失败: HTTP %d", e.Op, e.Status)
}

// USDTTransfer 是一笔打到收款地址的 USDT TRC20 转账。
type USDTTransfer struct {
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

type trc20Page struct {
	Data []struct {
		TransactionID  string `json:"transaction_id"`
		BlockTimestamp int64  `json:"block_timestamp"`
		BlockNumber    int64  `json:"block_number"`
		From           string `json:"from"`
		To             string `json:"to"`
		Type           string `json:"type"`
		Value          string `json:"value"`
		EventIndex     *int   `json:"event_index"`
		Confirmed      *bool  `json:"confirmed"`
		TokenInfo      struct {
			Address  string `json:"address"`
			Decimals int    `json:"decimals"`
		} `json:"token_info"`
	} `json:"data"`
	Success bool `json:"success"`
	Meta    struct {
		Fingerprint string `json:"fingerprint"`
	} `json:"meta"`
}

type solidityBlock struct {
	BlockHeader struct {
		RawData struct {
			Number    int64 `json:"number"`
			Timestamp int64 `json:"timestamp"`
		} `json:"raw_data"`
	} `json:"block_header"`
}

func (g *TronGrid) baseURL() string {
	base := strings.TrimRight(g.BaseURL, "/")
	if base == "" {
		return "https://api.trongrid.io"
	}
	return base
}

func (g *TronGrid) client() *http.Client {
	if g.HTTP != nil {
		return g.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (g *TronGrid) do(ctx context.Context, op, method, rawURL string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if g.APIKey != "" {
		req.Header.Set("TRON-PRO-API-KEY", g.APIKey)
	}
	resp, err := g.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("TronGrid %s 失败: %w", op, err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("TronGrid %s 读取失败: %w", op, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{Op: op, Status: resp.StatusCode}
	}
	return payload, nil
}

// SolidifiedHead 读取已固化区块头。只有不高于这个头的转账才能入账。
func (g *TronGrid) SolidifiedHead(ctx context.Context) (int64, time.Time, error) {
	payload, err := g.do(ctx, "getnowblock", http.MethodPost, g.baseURL()+"/walletsolidity/getnowblock", strings.NewReader(`{}`))
	if err != nil {
		return 0, time.Time{}, err
	}
	var body solidityBlock
	if err := json.Unmarshal(payload, &body); err != nil {
		return 0, time.Time{}, fmt.Errorf("固化区块格式错误: %w", err)
	}
	raw := body.BlockHeader.RawData
	if raw.Number <= 0 || raw.Timestamp <= 0 {
		return 0, time.Time{}, fmt.Errorf("固化区块头为空")
	}
	return raw.Number, time.UnixMilli(raw.Timestamp).UTC(), nil
}

// IncomingUSDT 拉取打到 address 的官方 USDT 转账，按区块时间升序。
// fingerprint 为空表示第一页。没有密钥时照常请求，只是额度更低。
func (g *TronGrid) IncomingUSDT(ctx context.Context, address string, since time.Time, fingerprint string) ([]USDTTransfer, string, error) {
	if !ValidAddress(address) {
		return nil, "", ErrInvalidAddress
	}
	q := url.Values{}
	q.Set("only_to", "true")
	q.Set("only_confirmed", "true")
	q.Set("contract_address", OfficialUSDT)
	q.Set("min_timestamp", strconv.FormatInt(since.UnixMilli(), 10))
	q.Set("limit", "200")
	q.Set("order_by", "block_timestamp,asc")
	if fingerprint != "" {
		q.Set("fingerprint", fingerprint)
	}
	rawURL := g.baseURL() + "/v1/accounts/" + url.PathEscape(address) + "/transactions/trc20?" + q.Encode()
	payload, err := g.do(ctx, "trc20", http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	var body trc20Page
	if err := json.Unmarshal(payload, &body); err != nil {
		return nil, "", fmt.Errorf("USDT 记录格式错误: %w", err)
	}
	if !body.Success && len(body.Data) == 0 && body.Meta.Fingerprint == "" {
		// 空账户有时不带 success。有数据却声明失败则不能当空页。
		if bytesLookLikeFailure(payload) {
			return nil, "", fmt.Errorf("USDT 记录被拒绝")
		}
	}
	out := make([]USDTTransfer, 0, len(body.Data))
	for _, row := range body.Data {
		if row.Confirmed != nil && !*row.Confirmed {
			continue
		}
		if row.Type != "Transfer" || row.TokenInfo.Address != OfficialUSDT || row.TokenInfo.Decimals != 6 {
			continue
		}
		if row.To != address || row.TransactionID == "" || row.BlockTimestamp <= 0 {
			continue
		}
		amount, err := strconv.ParseInt(row.Value, 10, 64)
		if err != nil || amount <= 0 {
			continue
		}
		idx := 0
		if row.EventIndex != nil && *row.EventIndex >= 0 {
			idx = *row.EventIndex
		}
		out = append(out, USDTTransfer{
			TxHash:      strings.ToLower(row.TransactionID),
			EventIndex:  idx,
			Contract:    OfficialUSDT,
			From:        row.From,
			To:          row.To,
			AmountMicro: amount,
			BlockNumber: row.BlockNumber,
			BlockTime:   time.UnixMilli(row.BlockTimestamp).UTC(),
			Confirmed:   true,
		})
	}
	return out, body.Meta.Fingerprint, nil
}

func bytesLookLikeFailure(payload []byte) bool {
	var probe struct {
		Success *bool `json:"success"`
	}
	if json.Unmarshal(payload, &probe) != nil || probe.Success == nil {
		return false
	}
	return !*probe.Success
}
