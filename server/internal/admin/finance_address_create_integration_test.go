package admin

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/tron"
)

func TestCreateReceiveAddressIsSingleAdminAndStoresNoKeyIntegration(t *testing.T) {
	f := newFinanceAddrFixture(t)
	role := pgx.Identifier{f.pool.Config().ConnConfig.User}.Sanitize()
	f.write(t, `ALTER TABLE receive_addresses ALTER COLUMN signature DROP NOT NULL`)
	f.write(t, `ALTER TABLE receive_addresses ALTER COLUMN sig_key_id DROP NOT NULL`)
	f.write(t, `ALTER TABLE receive_addresses DROP CONSTRAINT IF EXISTS receive_addresses_signature_pair`)
	f.write(t, `ALTER TABLE receive_addresses ADD CONSTRAINT receive_addresses_signature_pair
 CHECK ((signature IS NULL) = (sig_key_id IS NULL))`)
	f.write(t, `ALTER TABLE admin_finance_mutations DROP CONSTRAINT IF EXISTS admin_finance_mutations_action_code_check`)
	f.write(t, `ALTER TABLE admin_finance_mutations ADD CONSTRAINT admin_finance_mutations_action_code_check
 CHECK (action_code IN ('finance.address_status','finance.shared_pay_write','finance.address_create'))`)
	f.write(t, `INSERT INTO admin_permissions(code,name,risk_level) VALUES('finance.address_create','登记收款地址',2)
 ON CONFLICT(code) DO NOTHING`)
	f.write(t, `INSERT INTO admin_role_permissions(role_id,permission_code) VALUES($1,'finance.address_create')
 ON CONFLICT DO NOTHING`, f.role)
	f.write(t, "GRANT INSERT ON "+pgx.Identifier{f.schema, "receive_addresses"}.Sanitize()+" TO "+role)
	f.people[0].Permissions = append(f.people[0].Permissions, actionAddressCreate)

	in := CreateReceiveAddressInput{
		OperationID: uuid.NewString(),
		Reason:      "登记只收 USDT 的地址",
		Network:     "TRON",
		Address:     tron.OfficialUSDT,
		Label:       "官方合约仅作地址格式样例",
	}
	out, err := f.writer.CreateReceiveAddress(f.ctx, f.createProof(t, in), in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "PAUSED" || out.Network != "TRON" || out.Address != tron.OfficialUSDT {
		t.Fatalf("created %+v", out)
	}
	var sig, key *string
	var status string
	var id int64
	if err := f.owner.QueryRow(f.ctx, `SELECT id, signature, sig_key_id, status FROM receive_addresses WHERE address=$1`, tron.OfficialUSDT).
		Scan(&id, &sig, &key, &status); err != nil {
		t.Fatal(err)
	}
	if sig != nil || key != nil || status != "PAUSED" || strconv.FormatInt(id, 10) != out.ID {
		t.Fatalf("signature=%v key=%v status=%s id=%s", sig, key, status, out.ID)
	}
	var audits int
	var after string
	if err := f.owner.QueryRow(f.ctx, `SELECT count(*), COALESCE(max(after_data::text),'') FROM admin_audit_logs WHERE action_code='finance.address_create' AND target_id=$1`, id).Scan(&audits, &after); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("audits=%d", audits)
	}
	var snap map[string]string
	if err := json.Unmarshal([]byte(after), &snap); err != nil {
		t.Fatalf("audit snapshot %s: %v", after, err)
	}
	want := map[string]string{
		"target_id":     out.ID,
		"target_type":   "receive_address",
		"status":        "PAUSED",
		"decision_note": in.Reason,
	}
	if len(snap) != len(want) {
		t.Fatalf("audit snapshot fields %+v", snap)
	}
	for k, v := range want {
		if snap[k] != v {
			t.Fatalf("audit snapshot %s=%q, want %q; full %+v", k, snap[k], v, snap)
		}
	}

	again := in
	again.OperationID = uuid.NewString()
	_, err = f.writer.CreateReceiveAddress(f.ctx, f.createProof(t, again), again)
	var he *httpx.Error
	if !errorAs(err, &he) || he.Code != httpx.ErrConflict {
		t.Fatalf("duplicate: %v", err)
	}
}

func (f *financeAddrFixture) createProof(t *testing.T, in CreateReceiveAddressInput) ApprovalProof {
	t.Helper()
	path := "/admin/v1/receive-addresses"
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	p := f.proof(t, 0, actionAddressCreate, path, string(raw))
	p.RequestHash = RequestHash("POST", path, raw)
	f.write(t, `UPDATE admin_step_up_tokens SET payload_hash=$2 WHERE token_hash=$1`, tokenHash(p.Token), p.RequestHash)
	return p
}
