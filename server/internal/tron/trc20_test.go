package tron

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIncomingUSDTFiltersContractConfirmationsAndRateLimit(t *testing.T) {
	var gotKey, gotContract, gotConfirmed string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/walletsolidity/getnowblock":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"block_header":{"raw_data":{"number":99,"timestamp":1700000000000}}}`))
		case strings.Contains(r.URL.Path, "/transactions/trc20"):
			gotKey = r.Header.Get("TRON-PRO-API-KEY")
			gotContract = r.URL.Query().Get("contract_address")
			gotConfirmed = r.URL.Query().Get("only_confirmed")
			if r.URL.Query().Get("fingerprint") == "" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{
					"success": true,
					"data": [
						{"transaction_id":"AbC","block_timestamp":1700000001000,"from":"TFromxxxxxxxxxxxxxxxxxxxxxxxxxx","to":"` + OfficialUSDT + `","type":"Transfer","value":"6010000","token_info":{"address":"` + OfficialUSDT + `","decimals":6}},
						{"transaction_id":"unconfirmed","block_timestamp":1700000002000,"from":"TFromxxxxxxxxxxxxxxxxxxxxxxxxxx","to":"` + OfficialUSDT + `","type":"Transfer","value":"1000000","confirmed":false,"token_info":{"address":"` + OfficialUSDT + `","decimals":6}},
						{"transaction_id":"fake","block_timestamp":1700000003000,"from":"TFromxxxxxxxxxxxxxxxxxxxxxxxxxx","to":"` + OfficialUSDT + `","type":"Transfer","value":"1000000","token_info":{"address":"TFakeTokenxxxxxxxxxxxxxxxxxxxxx","decimals":6}},
						{"transaction_id":"decimals","block_timestamp":1700000004000,"from":"TFromxxxxxxxxxxxxxxxxxxxxxxxxxx","to":"` + OfficialUSDT + `","type":"Transfer","value":"1000000","token_info":{"address":"` + OfficialUSDT + `","decimals":18}}
					],
					"meta": {"fingerprint":"next"}
				}`))
				return
			}
			http.Error(w, "slow down", http.StatusTooManyRequests)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	g := &TronGrid{BaseURL: srv.URL, APIKey: "test-key", HTTP: srv.Client()}
	num, at, err := g.SolidifiedHead(context.Background())
	if err != nil || num != 99 || at.UnixMilli() != 1700000000000 {
		t.Fatalf("head %d %v %v", num, at, err)
	}
	rows, next, err := g.IncomingUSDT(context.Background(), OfficialUSDT, time.UnixMilli(1690000000000), "")
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != "test-key" || gotContract != OfficialUSDT || gotConfirmed != "true" || next != "next" {
		t.Fatalf("query key=%s contract=%s confirmed=%s next=%s", gotKey, gotContract, gotConfirmed, next)
	}
	if len(rows) != 1 || rows[0].TxHash != "abc" || rows[0].AmountMicro != 6_010_000 || !rows[0].Confirmed {
		t.Fatalf("rows %+v", rows)
	}
	_, _, err = g.IncomingUSDT(context.Background(), OfficialUSDT, time.UnixMilli(1690000000000), "next")
	var api *APIError
	if !errors.As(err, &api) || api.Status != http.StatusTooManyRequests {
		t.Fatalf("rate limit: %v", err)
	}
}

func TestIncomingUSDTWorksWithoutAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("TRON-PRO-API-KEY") != "" {
			t.Errorf("unexpected key")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":[],"meta":{}}`))
	}))
	defer srv.Close()
	g := &TronGrid{BaseURL: srv.URL, HTTP: srv.Client()}
	rows, next, err := g.IncomingUSDT(context.Background(), OfficialUSDT, time.UnixMilli(1), "")
	if err != nil || len(rows) != 0 || next != "" {
		t.Fatalf("rows=%d next=%s err=%v", len(rows), next, err)
	}
}
