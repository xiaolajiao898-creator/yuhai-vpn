package admin

import (
	"context"
	"testing"

	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/tron"
)

func TestCreateReceiveAddressRejectsKeysAndBadNetworks(t *testing.T) {
	svc := &FinanceAddressService{}
	ctx := context.Background()
	_, err := svc.CreateReceiveAddress(ctx, ApprovalProof{}, CreateReceiveAddressInput{
		Network: "TRON", Address: "not-an-address", Reason: "登记", OperationID: "00000000-0000-4000-8000-000000000001",
	})
	var he *httpx.Error
	if !errorAs(err, &he) || he.Code != httpx.ErrBadRequest {
		t.Fatalf("bad tron: %v", err)
	}
	_, err = svc.CreateReceiveAddress(ctx, ApprovalProof{}, CreateReceiveAddressInput{
		Network: "MOCK", Address: tron.OfficialUSDT, Reason: "登记", OperationID: "00000000-0000-4000-8000-000000000001",
	})
	if !errorAs(err, &he) || he.Code != httpx.ErrBadRequest {
		t.Fatalf("mock real address: %v", err)
	}
	_, err = svc.CreateReceiveAddress(ctx, ApprovalProof{}, CreateReceiveAddressInput{
		Network: "ETH", Address: "abc", Reason: "登记", OperationID: "00000000-0000-4000-8000-000000000001",
	})
	if !errorAs(err, &he) || he.Code != httpx.ErrBadRequest {
		t.Fatalf("network: %v", err)
	}
	// 校验通过后才碰到审批服务。这里没有审批服务，所以停在内部错误，而不会去写库。
	_, err = svc.CreateReceiveAddress(ctx, ApprovalProof{}, CreateReceiveAddressInput{
		Network: "TRON", Address: tron.OfficialUSDT, Reason: "登记", OperationID: "00000000-0000-4000-8000-000000000001",
	})
	if err != httpx.Internal {
		t.Fatalf("valid address should reach the approval gate, got %v", err)
	}
}

func errorAs(err error, target **httpx.Error) bool {
	if err == nil {
		return false
	}
	e, ok := err.(*httpx.Error)
	if !ok {
		return false
	}
	*target = e
	return true
}
