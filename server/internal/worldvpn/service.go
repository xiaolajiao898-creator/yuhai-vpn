package worldvpn

import (
	"context"
	"crypto/rand"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/httpx"
)

const materialLifetime = 5 * time.Minute

// upstream 是批发 HTTP API。*Client 实现它；测试用假的。
type upstream interface {
	AddVPN(ctx context.Context, username, password string, months int) (vpnAccount, error)
	RenewVPN(ctx context.Context, username string, months int) error
	DeleteVPN(ctx context.Context, username string, sandbox bool) error
	GetTrial(ctx context.Context, email string) (vpnAccount, error)
	ListServers(ctx context.Context) ([]serverLine, error)
	AddWireGuardPeer(ctx context.Context, username string, serverID int64) (peerCreated, error)
	GetWireGuardConfig(ctx context.Context, username, wireguardID string) (wireGuardMaterial, error)
	DeleteWireGuardPeer(ctx context.Context, username, wireguardID string, sandbox bool) error
}

type account struct {
	UserID   int64
	Username string
	Password string
	Status   string
	ExpireAt time.Time
	Source   string
	Sandbox  bool
}

type grant struct {
	ID     int64
	UserID int64
	Months int
	Source string
}

type openPeer struct {
	ID          int64
	UserID      int64
	DeviceID    int64
	ServerID    int64
	WireGuardID string
	Sandbox     bool
}

type lineAccess struct {
	Available bool
	Reason    string
}

// Store 是本地账本。口令以明文进出这一层，由 PG 实现负责加密，调用方不得写日志。
type Store interface {
	EnsureSyncAfter(ctx context.Context, now time.Time) (time.Time, error)
	CollectOrders(ctx context.Context, after time.Time) error
	PendingGrants(ctx context.Context, limit int) ([]grant, error)
	FinishGrant(ctx context.Context, id int64, status, lastErr string) error
	UserAccountNo(ctx context.Context, userID int64) (string, error)
	SubscriptionExpire(ctx context.Context, userID int64) (time.Time, bool, error)
	Account(ctx context.Context, userID int64) (account, bool, error)
	SaveAccount(ctx context.Context, a account) error
	AccountsToDisable(ctx context.Context, now time.Time) ([]account, error)
	MarkDisabled(ctx context.Context, userID int64) error
	PeersToRelease(ctx context.Context) ([]openPeer, error)
	MarkPeerReleased(ctx context.Context, id int64) error
	LineAccess(ctx context.Context, userID int64, now time.Time, sandbox bool) (lineAccess, error)
	DeviceOK(ctx context.Context, userID, deviceID int64) error
	ActivePeers(ctx context.Context, userID, exceptDevice int64) (int, error)
	DevicePeer(ctx context.Context, userID, deviceID int64) (openPeer, bool, error)
	InsertPeer(ctx context.Context, p openPeer) error
	RequestRelease(ctx context.Context, userID, deviceID int64) (openPeer, bool, error)
}

// Service 把宇海的开通、线路和连接对上批发 API。
type Service struct {
	Settings Settings
	API      upstream
	Store    Store
	Clock    clock.Clock
	Pepper   []byte
}

func (s *Service) now() time.Time {
	if s.Clock == nil {
		return time.Now().UTC()
	}
	return s.Clock.Now().UTC()
}

func (s *Service) enabled() error {
	if s == nil || s.API == nil || s.Store == nil || !s.Settings.Enabled() {
		return httpx.NewError(httpx.ErrMaintenance, "批发线路未配置")
	}
	return nil
}

// Run 由 worker 周期调用：为已支付的时长订单开/续账号，停用到期或被封禁的账号，并补删断开的 peer。
func (s *Service) Run(ctx context.Context) error {
	if err := s.enabled(); err != nil {
		return nil
	}
	now := s.now()
	after := s.Settings.SyncAfter
	if after.IsZero() {
		var err error
		after, err = s.Store.EnsureSyncAfter(ctx, now)
		if err != nil {
			return err
		}
	}
	if err := s.Store.CollectOrders(ctx, after); err != nil {
		return err
	}
	grants, err := s.Store.PendingGrants(ctx, 20)
	if err != nil {
		return err
	}
	for _, g := range grants {
		if err := s.applyGrant(ctx, g, now); err != nil {
			_ = s.Store.FinishGrant(ctx, g.ID, "PENDING", clipErr(err))
		}
	}
	if err := s.disableExpired(ctx, now); err != nil {
		return err
	}
	return s.sweepPeers(ctx)
}

func (s *Service) applyGrant(ctx context.Context, g grant, now time.Time) error {
	if g.Months == 0 {
		return s.applyTrial(ctx, g, now)
	}
	if _, ok := PlanCodeForMonths(g.Months); !ok {
		return s.Store.FinishGrant(ctx, g.ID, "SKIPPED", "duration not a wholesale month")
	}
	no, err := s.Store.UserAccountNo(ctx, g.UserID)
	if err != nil {
		return err
	}
	username, err := localUsername(no)
	if err != nil {
		return err
	}
	password, err := randomPassword()
	if err != nil {
		return err
	}
	current, ok, err := s.Store.Account(ctx, g.UserID)
	if err != nil {
		return err
	}
	reuse := ok && current.Status == "ACTIVE" && current.Sandbox == s.Settings.Sandbox && current.ExpireAt.After(now.Add(-time.Hour))
	if reuse {
		username = current.Username
		password = current.Password
		if err := s.API.RenewVPN(ctx, username, g.Months); err != nil {
			return err
		}
	} else {
		created, err := s.API.AddVPN(ctx, username, password, g.Months)
		if err != nil {
			if !alreadyExists(err) {
				return err
			}
			if err := s.API.RenewVPN(ctx, username, g.Months); err != nil {
				return err
			}
		} else if !s.Settings.Sandbox {
			username, password = created.Username, created.Password
		}
	}
	exp, has, err := s.Store.SubscriptionExpire(ctx, g.UserID)
	if err != nil {
		return err
	}
	if !has || !exp.After(now) {
		exp = now.AddDate(0, 0, 30*g.Months)
	}
	source := g.Source
	if source == "" {
		source = "ORDER"
	}
	if err := s.Store.SaveAccount(ctx, account{
		UserID: g.UserID, Username: username, Password: password, Status: "ACTIVE",
		ExpireAt: exp, Source: source, Sandbox: s.Settings.Sandbox,
	}); err != nil {
		return err
	}
	return s.Store.FinishGrant(ctx, g.ID, "DONE", "")
}

// applyTrial 跟着现有的 24 小时试用走。用户表没有邮箱，只向批发接口传一个不会投递的占位地址。
func (s *Service) applyTrial(ctx context.Context, g grant, now time.Time) error {
	current, ok, err := s.Store.Account(ctx, g.UserID)
	if err != nil {
		return err
	}
	if ok && current.Status == "ACTIVE" && current.Sandbox == s.Settings.Sandbox && current.ExpireAt.After(now) {
		return s.Store.FinishGrant(ctx, g.ID, "DONE", "")
	}
	no, err := s.Store.UserAccountNo(ctx, g.UserID)
	if err != nil {
		return err
	}
	username, err := localUsername(no)
	if err != nil {
		return err
	}
	password, err := randomPassword()
	if err != nil {
		return err
	}
	if _, err := s.API.GetTrial(ctx, "trial-"+username+"@users.invalid"); err != nil {
		return err
	}
	exp := now.Add(24 * time.Hour)
	if sub, has, err := s.Store.SubscriptionExpire(ctx, g.UserID); err != nil {
		return err
	} else if has && sub.After(now) && sub.Before(exp) {
		exp = sub
	}
	if err := s.Store.SaveAccount(ctx, account{
		UserID: g.UserID, Username: username, Password: password, Status: "ACTIVE",
		ExpireAt: exp, Source: "TRIAL", Sandbox: s.Settings.Sandbox,
	}); err != nil {
		return err
	}
	return s.Store.FinishGrant(ctx, g.ID, "DONE", "")
}

func (s *Service) disableExpired(ctx context.Context, now time.Time) error {
	rows, err := s.Store.AccountsToDisable(ctx, now)
	if err != nil {
		return err
	}
	for _, acc := range rows {
		if !(acc.Sandbox && !s.Settings.Sandbox) {
			if err := s.API.DeleteVPN(ctx, acc.Username, acc.Sandbox); err != nil && !alreadyGone(err) {
				continue
			}
		}
		if err := s.Store.MarkDisabled(ctx, acc.UserID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) sweepPeers(ctx context.Context) error {
	rows, err := s.Store.PeersToRelease(ctx)
	if err != nil {
		return err
	}
	for _, p := range rows {
		if p.Sandbox && !s.Settings.Sandbox {
			if err := s.Store.MarkPeerReleased(ctx, p.ID); err != nil {
				return err
			}
			continue
		}
		acc, ok, err := s.Store.Account(ctx, p.UserID)
		if err != nil {
			return err
		}
		if ok {
			if err := s.API.DeleteWireGuardPeer(ctx, acc.Username, p.WireGuardID, p.Sandbox); err != nil && !alreadyGone(err) {
				continue
			}
		}
		if err := s.Store.MarkPeerReleased(ctx, p.ID); err != nil {
			return err
		}
	}
	return nil
}

// Line 是给 App 的一条线路。不含批发口令。可用时带探测地址，连接材料另取。
type Line struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	CountryCode  string  `json:"country_code"`
	Region       string  `json:"region"`
	Available    bool    `json:"available"`
	LockedReason *string `json:"locked_reason"`
	Probe        *Probe  `json:"probe"`
}

type Probe struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

func (s *Service) Lines(ctx context.Context, userID int64) ([]Line, error) {
	if err := s.enabled(); err != nil {
		return nil, err
	}
	servers, err := s.API.ListServers(ctx)
	if err != nil {
		return nil, httpx.NewError(httpx.ErrNodeUnavailable, "批发线路暂时不可用")
	}
	access, err := s.Store.LineAccess(ctx, userID, s.now(), s.Settings.Sandbox)
	if err != nil {
		return nil, err
	}
	out := make([]Line, 0, len(servers))
	for _, sv := range servers {
		cc, region := PlaceOf(sv.Name)
		line := Line{ID: "w" + itoa(sv.ID), Name: sv.Name, CountryCode: cc, Region: region}
		if access.Available {
			line.Available = true
			line.Probe = &Probe{Host: sv.IP, Port: ProbePort}
		} else {
			reason := access.Reason
			line.LockedReason = &reason
		}
		out = append(out, line)
	}
	return out, nil
}

// Issued 是交给现有 WireGuard 引擎的一份配置。私钥只出现在这次响应里，不入库。
type Issued struct {
	LineID              string   `json:"line_id"`
	ServerID            string   `json:"server_id"`
	ExpiresAt           string   `json:"expires_at"`
	Endpoint            string   `json:"endpoint"`
	ServerPublicKey     string   `json:"server_public_key"`
	Address             string   `json:"address"`
	DNS                 []string `json:"dns"`
	MTU                 int      `json:"mtu"`
	InterfacePrivateKey string   `json:"interface_private_key"`
}

func (s *Service) Connect(ctx context.Context, userID, deviceID int64, lineID string) (Issued, error) {
	var out Issued
	if err := s.enabled(); err != nil {
		return out, err
	}
	serverID, err := parseLineID(lineID)
	if err != nil {
		return out, err
	}
	if err := s.Store.DeviceOK(ctx, userID, deviceID); err != nil {
		return out, err
	}
	now := s.now()
	acc, ok, err := s.Store.Account(ctx, userID)
	if err != nil {
		return out, err
	}
	if !ok || acc.Status != "ACTIVE" || !acc.ExpireAt.After(now) || acc.Sandbox != s.Settings.Sandbox {
		return out, httpx.NewError(httpx.ErrNoPlan, "没有可用套餐")
	}
	if existing, found, err := s.Store.DevicePeer(ctx, userID, deviceID); err != nil {
		return out, err
	} else if found {
		if err := s.API.DeleteWireGuardPeer(ctx, acc.Username, existing.WireGuardID, existing.Sandbox); err != nil && !alreadyGone(err) {
			return out, httpx.NewError(httpx.ErrNodeUnavailable, "上一条线路还没释放")
		}
		if err := s.Store.MarkPeerReleased(ctx, existing.ID); err != nil {
			return out, err
		}
	}
	n, err := s.Store.ActivePeers(ctx, userID, deviceID)
	if err != nil {
		return out, err
	}
	if n >= MaxActivePeersPerUser {
		return out, httpx.NewError(httpx.ErrDeviceLimit, "同时在线设备已达 3 台上限")
	}
	created, err := s.API.AddWireGuardPeer(ctx, acc.Username, serverID)
	if err != nil {
		return out, httpx.NewError(httpx.ErrNodeUnavailable, "批发线路暂时不可用")
	}
	material, err := s.API.GetWireGuardConfig(ctx, acc.Username, created.WireGuardID)
	if err != nil {
		_ = s.API.DeleteWireGuardPeer(ctx, acc.Username, created.WireGuardID, s.Settings.Sandbox)
		return out, httpx.NewError(httpx.ErrNodeUnavailable, "批发线路暂时不可用")
	}
	if err := s.Store.InsertPeer(ctx, openPeer{
		UserID: userID, DeviceID: deviceID, ServerID: serverID,
		WireGuardID: created.WireGuardID, Sandbox: s.Settings.Sandbox,
	}); err != nil {
		_ = s.API.DeleteWireGuardPeer(ctx, acc.Username, created.WireGuardID, s.Settings.Sandbox)
		return out, err
	}
	exp := now.Add(materialLifetime).UTC().Truncate(time.Second)
	if acc.ExpireAt.Before(exp) {
		exp = acc.ExpireAt.UTC().Truncate(time.Second)
	}
	if !exp.After(now) {
		exp = now.Add(time.Second).UTC().Truncate(time.Second)
	}
	return Issued{
		LineID: lineID, ServerID: itoa(serverID), ExpiresAt: exp.UTC().Format("2006-01-02T15:04:05Z"),
		Endpoint: material.Endpoint, ServerPublicKey: material.ServerPublicKey, Address: material.Address,
		DNS: material.DNS, MTU: material.MTU, InterfacePrivateKey: material.PrivateKey,
	}, nil
}

// Release 尽力删除本设备当前的 peer。删不掉会留给 worker 重试，不让断开本身失败。
func (s *Service) Release(ctx context.Context, userID, deviceID int64) error {
	if err := s.enabled(); err != nil {
		return nil
	}
	p, ok, err := s.Store.RequestRelease(ctx, userID, deviceID)
	if err != nil || !ok {
		return err
	}
	acc, found, err := s.Store.Account(ctx, userID)
	if err != nil {
		return err
	}
	if found && !(p.Sandbox && !s.Settings.Sandbox) {
		if err := s.API.DeleteWireGuardPeer(ctx, acc.Username, p.WireGuardID, p.Sandbox); err != nil && !alreadyGone(err) {
			return nil
		}
	}
	return s.Store.MarkPeerReleased(ctx, p.ID)
}

var accountNoPattern = regexp.MustCompile(`^[A-Za-z0-9]{4,16}$`)

func localUsername(accountNo string) (string, error) {
	accountNo = strings.TrimSpace(accountNo)
	if !accountNoPattern.MatchString(accountNo) {
		return "", errors.New("worldvpn: 账号编号不能用作批发用户名")
	}
	return "yh" + accountNo, nil
}

func randomPassword() (string, error) {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	out := make([]byte, len(raw))
	for i, b := range raw {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(out), nil
}

func parseLineID(id string) (int64, error) {
	if !strings.HasPrefix(id, "w") {
		return 0, httpx.NewError(httpx.ErrBadRequest, "线路编号无效")
	}
	n, err := strconv.ParseInt(id[1:], 10, 64)
	if err != nil || n <= 0 || "w"+strconv.FormatInt(n, 10) != id {
		return 0, httpx.NewError(httpx.ErrBadRequest, "线路编号无效")
	}
	return n, nil
}

func alreadyExists(err error) bool {
	var api *APIError
	if !errors.As(err, &api) {
		return false
	}
	msg := strings.ToLower(api.Message)
	return strings.Contains(msg, "exist") || strings.Contains(msg, "already")
}

func alreadyGone(err error) bool {
	var api *APIError
	if !errors.As(err, &api) {
		return false
	}
	msg := strings.ToLower(api.Message)
	return strings.Contains(msg, "not found") || strings.Contains(msg, "not exist") || strings.Contains(msg, "no such")
}

func clipErr(err error) string {
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}
