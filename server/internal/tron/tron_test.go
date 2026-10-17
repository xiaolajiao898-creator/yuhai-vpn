package tron

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// USDT 合约地址是公开的已知值，用来校验编解码。
const usdt = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"

func TestAddressRoundTrip(t *testing.T) {
	p, err := DecodeAddress(usdt)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(p); got != "41a614f803b6fd780986a42c78ec9c7f77e6ded13c" {
		t.Fatalf("hex = %s", got)
	}
	if EncodeAddress(p) != usdt {
		t.Fatal("round trip mismatch")
	}
	for _, bad := range []string{"", "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6u", "0x1234", "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6", "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6O"} {
		if ValidAddress(bad) {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestTronGridFind(t *testing.T) {
	from := usdt
	toPayload := make([]byte, 21)
	toPayload[0] = 0x41
	for i := 1; i < 21; i++ {
		toPayload[i] = byte(0x5a + i)
	}
	to := EncodeAddress(toPayload)
	if !ValidAddress(to) {
		t.Fatalf("generated %s invalid", to)
	}
	fromHex, _ := HexAddress(from)
	toHex, _ := HexAddress(to)
	since := time.Unix(1_790_000_000, 0)
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("TRON-PRO-API-KEY")
		if r.URL.Query().Get("only_from") != "true" || r.URL.Query().Get("only_confirmed") != "true" {
			t.Error("missing filters")
		}
		tx := func(id string, amount int64, ts int64, owner, dest, ret string) string {
			return fmt.Sprintf(`{"txID":%q,"block_timestamp":%d,"ret":[{"contractRet":%q}],"raw_data":{"contract":[{"type":"TransferContract","parameter":{"value":{"amount":%d,"owner_address":%q,"to_address":%q}}}]}}`, id, ts, ret, amount, owner, dest)
		}
		ms := since.UnixMilli()
		fmt.Fprintf(w, `{"success":true,"data":[%s,%s,%s,%s,%s]}`,
			tx("AA01", 1_004_372, ms+1000, fromHex, toHex, "REVERT"),    // 失败交易
			tx("AA02", 1_000_000, ms+1000, fromHex, toHex, "SUCCESS"),   // 金额不符
			tx("AA03", 1_004_372, ms-1000, fromHex, toHex, "SUCCESS"),   // 早于挑战
			tx("AA04", 1_004_372, ms+2000, fromHex, fromHex, "SUCCESS"), // 收款方不符
			tx("AA05", 1_004_372, ms+3000, fromHex, toHex, "SUCCESS"))
	}))
	defer srv.Close()
	g := &TronGrid{BaseURL: srv.URL, APIKey: "k1"}
	tr, err := g.FindTRXTransfer(context.Background(), from, to, 1_004_372, since)
	if err != nil || tr.TxID != "aa05" || gotKey != "k1" {
		t.Fatalf("%+v %v %q", tr, err, gotKey)
	}
	if _, err := g.FindTRXTransfer(context.Background(), from, to, 1_000_001, since); !errors.Is(err, ErrTransferNotFound) {
		t.Fatal("expected not found", err)
	}
}
