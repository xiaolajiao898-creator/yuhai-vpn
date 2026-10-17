package admin

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/yuhai-vpn/server/internal/httpx"
)

// 设备总览要钉住的是它与「按用户查设备」的差别，以及那些差别**没有**带来的东西：
//
//   - 跨用户能看到全部设备，每行必须带 user_id（否则总览没有意义）；
//   - 投影仍然与按用户查那份一致，一个字段都不多；
//   - 筛选值非法一律 400，不静默忽略——静默忽略会让「只看已撤销」变成「看全部」；
//   - 查无此人返回空列表而不是 404，避免用户编号被枚举。

func TestDeviceOverviewSpansUsersAndKeepsProjectionIntegration(t *testing.T) {
	f := newManagedUserFixture(t)
	p := f.people[0]
	// 夹具已给 userID 建了 3 台（2 ACTIVE + 1 REVOKED）。再给另一个用户建一台 IOS。
	f.write(t, `INSERT INTO devices(user_id,device_uid,pubkey,platform,fingerprint_hash,status,created_at)
 VALUES($1,$2,'public-key-sentinel','IOS','`+
		`ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff','ACTIVE',$3)`,
		f.emptyID, uuid.NewString(), f.clock.Now())

	out, err := f.service.Overview(f.ctx, p, ManagedDeviceOverviewFilter{})
	if err != nil || out.Total != "4" || len(out.Items) != 4 {
		t.Fatal("跨用户总览", out, err)
	}
	owners := map[string]int{}
	for _, d := range out.Items {
		if d.UserID == "" {
			t.Fatal("总览行缺少 user_id", d)
		}
		owners[d.UserID]++
	}
	if len(owners) != 2 {
		t.Fatal("没有跨到两个用户", owners)
	}
	if out.ObservedAt.IsZero() {
		t.Fatal("缺少快照时间")
	}

	// 与按用户查那份逐字段一致：同一台设备在两个入口上的投影必须相同。
	one, err := f.service.Devices(f.ctx, p, f.emptyID, ManagedDeviceFilter{})
	if err != nil || len(one.Items) != 1 {
		t.Fatal("按用户查", one, err)
	}
	byUser, err := f.service.Overview(f.ctx, p, ManagedDeviceOverviewFilter{UserID: "5"})
	if err != nil || len(byUser.Items) != 1 || byUser.Items[0].UserID != "5" {
		t.Fatal("按 user_id 精确筛选", byUser, err)
	}
	if byUser.Items[0].ManagedDevice != one.Items[0] {
		t.Fatal("两个入口的投影不一致", byUser.Items[0].ManagedDevice, one.Items[0])
	}

	revoked, err := f.service.Overview(f.ctx, p, ManagedDeviceOverviewFilter{Status: "REVOKED"})
	if err != nil || revoked.Total != "1" || revoked.Items[0].Status != "REVOKED" {
		t.Fatal("按状态筛选", revoked, err)
	}
	ios, err := f.service.Overview(f.ctx, p, ManagedDeviceOverviewFilter{Platform: "IOS"})
	if err != nil || ios.Total != "1" || ios.Items[0].Platform != "IOS" {
		t.Fatal("按平台筛选", ios, err)
	}
	// 两个筛选同时生效，而不是后一个覆盖前一个。
	none, err := f.service.Overview(f.ctx, p, ManagedDeviceOverviewFilter{Platform: "IOS", Status: "REVOKED"})
	if err != nil || none.Total != "0" || len(none.Items) != 0 {
		t.Fatal("组合筛选", none, err)
	}
}

func TestDeviceOverviewRejectsBadFiltersIntegration(t *testing.T) {
	f := newManagedUserFixture(t)
	p := f.people[0]
	for name, filter := range map[string]ManagedDeviceOverviewFilter{
		"状态非法":    {Status: "DELETED"},
		"状态大小写":   {Status: "active"},
		"平台非法":    {Platform: "HARMONY"},
		"用户编号零":   {UserID: "0"},
		"用户编号非数字": {UserID: "abc"},
		"页码越界":    {Page: 10001},
		"每页过大":    {PageSize: 51},
	} {
		_, err := f.service.Overview(f.ctx, p, filter)
		var coded *httpx.Error
		if !errors.As(err, &coded) || coded.Code != httpx.ErrBadRequest {
			t.Fatalf("%s 应当被拒绝，得到 %v", name, err)
		}
	}
	// 合法但不存在的用户：空列表，不是 404——否则用户编号就成了可枚举的信号。
	out, err := f.service.Overview(f.ctx, p, ManagedDeviceOverviewFilter{UserID: "9007199254740999"})
	if err != nil || out.Total != "0" || len(out.Items) != 0 {
		t.Fatal("不存在的用户应当返回空列表", out, err)
	}
}

func TestDeviceOverviewRequiresUsersReadIntegration(t *testing.T) {
	f := newManagedUserFixture(t)
	p := f.people[0]
	f.write(t, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='users.read'`, f.role)
	_, err := f.service.Overview(f.ctx, p, ManagedDeviceOverviewFilter{})
	nodeCode(t, err, httpx.ErrForbidden)
}
