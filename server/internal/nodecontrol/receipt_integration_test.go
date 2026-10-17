package nodecontrol

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// 撤销回执的价值全在「什么时候不写」。下面每个用例都盯着一种不该被当作回执的情形：
// 节点没收敛、心跳陈旧、没上报校验和、凭据其实还在清单里、以及重复执行。

func newReceiptFixture(t *testing.T) (*heartbeatFixture, *RemovalReceipts) {
	t.Helper()
	f := newWGFixture(t)
	f.exec(t, "CREATE TABLE "+pgx.Identifier{"node_user_events"}.Sanitize()+
		" (LIKE "+pgx.Identifier{"public", "node_user_events"}.Sanitize()+" INCLUDING ALL)")

	// 用真实迁移建回执表，只去掉其中的权限登记（测试 schema 里没有 admin_permissions）。
	raw, err := os.ReadFile("../db/migrations/00021_node_credential_receipts.sql")
	if err != nil {
		t.Fatal(err)
	}
	ddl := strings.Split(strings.Split(string(raw), "-- +goose Down")[0], "INSERT INTO admin_permissions")[0]
	if _, err := f.owner.Exec(f.ctx, ddl); err != nil {
		t.Fatal("实际迁移执行失败", err)
	}
	role := pgx.Identifier{f.pool.Config().ConnConfig.User}.Sanitize()
	for _, sql := range []string{
		"GRANT SELECT ON node_user_events TO " + role,
		"GRANT SELECT,INSERT ON node_credential_receipts TO " + role,
	} {
		f.exec(t, sql)
	}
	return f, &RemovalReceipts{Pool: f.pool, Clock: f.clock, Enabled: true}
}

// revoke 把某凭据置为已撤销，并登记「期望从节点移除」的事件。
func revoke(t *testing.T, f *heartbeatFixture, credentialID int64) {
	t.Helper()
	f.exec(t, `UPDATE device_credentials SET status='REVOKED' WHERE id=$1`, credentialID)
	f.exec(t, `INSERT INTO node_user_events(credential_id,effective_tier,reason,created_at) VALUES($1,0,'REVOKE',$2)`,
		credentialID, f.clock.Now())
}

// beat 让节点上报一次心跳，校验和由调用方给定。
func beat(t *testing.T, f *heartbeatFixture, seq, checksum string) {
	t.Helper()
	h := heartbeatAt(f.clock, seq)
	h.SnapshotChecksum = checksum
	// 上报就绪协议要求 config_version 非零且校验和为 64 位，与 fixture 的入站配置一致。
	h.ConfigVersion = "1"
	h.ReadyProtocols = []string{"WIREGUARD"}
	if _, err := f.svc.Accept(f.ctx, f.ca.state(f.cert), h); err != nil {
		t.Fatal(err)
	}
}

func receiptCount(t *testing.T, f *heartbeatFixture) int {
	t.Helper()
	var n int
	if err := f.owner.QueryRow(f.ctx, `SELECT count(*) FROM node_credential_receipts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRemovalReceiptConfirmsConvergedNodeIntegration(t *testing.T) {
	f, r := newReceiptFixture(t)
	addWGUser(t, f, 11)
	addWGUser(t, f, 12)
	revoke(t, f, 11)

	// 撤销之后的当前期望状态：11 已不在清单中。
	lease := getWG(t, f)
	if len(lease.Manifest.Peers) != 1 || lease.Manifest.Peers[0].CredentialID != "12" {
		t.Fatalf("撤销后清单不符：%v", lease.Manifest.Peers)
	}
	beat(t, f, "1", lease.Checksum)

	n, err := r.ConfirmOnce(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应当写入 1 条回执，实际 %d", n)
	}
	var credential, sequence int64
	var checksum string
	var at time.Time
	if err := f.owner.QueryRow(f.ctx,
		`SELECT credential_id,heartbeat_sequence,snapshot_checksum,confirmed_absent_at FROM node_credential_receipts`,
	).Scan(&credential, &sequence, &checksum, &at); err != nil {
		t.Fatal(err)
	}
	if credential != 11 || sequence != 1 || checksum != lease.Checksum {
		t.Fatalf("回执内容不符：credential=%d sequence=%d checksum=%s", credential, sequence, checksum)
	}
	if !at.Equal(f.clock.Now()) {
		t.Fatalf("确认时间不是当前时钟：%s vs %s", at, f.clock.Now())
	}
}

func TestRemovalReceiptIgnoresCredentialStillInManifestIntegration(t *testing.T) {
	f, r := newReceiptFixture(t)
	addWGUser(t, f, 21)
	// 只登记移除事件但凭据仍然有效——清单里还在，就不能说它已被移除。
	f.exec(t, `INSERT INTO node_user_events(credential_id,effective_tier,reason,created_at) VALUES($1,0,'REVOKE',$2)`,
		int64(21), f.clock.Now())

	lease := getWG(t, f)
	if len(lease.Manifest.Peers) != 1 {
		t.Fatalf("凭据仍有效时清单应当包含它：%v", lease.Manifest.Peers)
	}
	beat(t, f, "1", lease.Checksum)

	n, err := r.ConfirmOnce(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 || receiptCount(t, f) != 0 {
		t.Fatal("凭据仍在清单中却写出了回执")
	}
}

func TestRemovalReceiptRefusesUnconfirmedStatesIntegration(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, f *heartbeatFixture, lease string)
	}{
		{"节点尚未收敛（校验和不符）", func(t *testing.T, f *heartbeatFixture, _ string) {
			beat(t, f, "1", strings.Repeat("a", 64))
		}},
		{"节点未上报校验和", func(t *testing.T, f *heartbeatFixture, _ string) {
			h := heartbeatAt(f.clock, "1")
			h.SnapshotChecksum = ""
			if _, err := f.svc.Accept(f.ctx, f.ca.state(f.cert), h); err != nil {
				t.Fatal(err)
			}
		}},
		{"心跳已陈旧", func(t *testing.T, f *heartbeatFixture, lease string) {
			beat(t, f, "1", lease)
			f.clock.Add(91 * time.Second) // 超出 90 秒新鲜度窗口
		}},
		{"从未收到心跳", func(t *testing.T, f *heartbeatFixture, _ string) {}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, r := newReceiptFixture(t)
			addWGUser(t, f, 31)
			addWGUser(t, f, 32)
			revoke(t, f, 31)
			lease := getWG(t, f)
			c.setup(t, f, lease.Checksum)

			n, err := r.ConfirmOnce(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if n != 0 || receiptCount(t, f) != 0 {
				t.Fatal("在不应确认的状态下写出了回执")
			}
		})
	}
}

func TestRemovalReceiptIsIdempotentIntegration(t *testing.T) {
	f, r := newReceiptFixture(t)
	addWGUser(t, f, 41)
	addWGUser(t, f, 42)
	revoke(t, f, 41)
	lease := getWG(t, f)
	beat(t, f, "1", lease.Checksum)

	first, err := r.ConfirmOnce(f.ctx)
	if err != nil || first != 1 {
		t.Fatal(first, err)
	}
	var at time.Time
	if err := f.owner.QueryRow(f.ctx, `SELECT confirmed_absent_at FROM node_credential_receipts`).Scan(&at); err != nil {
		t.Fatal(err)
	}

	// 时钟前进后再跑一轮：不得新增，也不得改写首次确认时间。
	f.clock.Add(10 * time.Second)
	beat(t, f, "2", lease.Checksum)
	second, err := r.ConfirmOnce(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second != 0 || receiptCount(t, f) != 1 {
		t.Fatalf("重复执行写出了额外回执：second=%d total=%d", second, receiptCount(t, f))
	}
	var again time.Time
	if err := f.owner.QueryRow(f.ctx, `SELECT confirmed_absent_at FROM node_credential_receipts`).Scan(&again); err != nil {
		t.Fatal(err)
	}
	if !again.Equal(at) {
		t.Fatal("重复执行改写了首次确认时间")
	}
}

func TestRemovalReceiptDisabledDoesNothingIntegration(t *testing.T) {
	f, r := newReceiptFixture(t)
	r.Enabled = false
	addWGUser(t, f, 51)
	addWGUser(t, f, 52)
	revoke(t, f, 51)
	beat(t, f, "1", getWG(t, f).Checksum)

	n, err := r.ConfirmOnce(f.ctx)
	if err != nil || n != 0 {
		t.Fatal("关闭时仍有动作", n, err)
	}
	if receiptCount(t, f) != 0 {
		t.Fatal("关闭时写出了回执")
	}
}

func TestRemovalReceiptConfirmsMultipleCredentialsIntegration(t *testing.T) {
	f, r := newReceiptFixture(t)
	for _, id := range []int{61, 62, 63} {
		addWGUser(t, f, id)
	}
	revoke(t, f, 61)
	revoke(t, f, 63)
	lease := getWG(t, f)
	if len(lease.Manifest.Peers) != 1 || lease.Manifest.Peers[0].CredentialID != "62" {
		t.Fatalf("清单不符：%v", lease.Manifest.Peers)
	}
	beat(t, f, "1", lease.Checksum)

	n, err := r.ConfirmOnce(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("应当写入 2 条回执，实际 %d", n)
	}
	rows, err := f.owner.Query(f.ctx, `SELECT credential_id FROM node_credential_receipts ORDER BY credential_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		got = append(got, id)
	}
	if len(got) != 2 || got[0] != 61 || got[1] != 63 {
		t.Fatalf("回执覆盖的凭据不符：%v", got)
	}
}
