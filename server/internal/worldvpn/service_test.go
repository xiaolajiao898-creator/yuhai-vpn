package worldvpn

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/httpx"
)

type fakeAPI struct {
	adds, renews, deletes, peerAdds, peerDels, trials int
	renewUsers                                        []string
	addUsers                                          []string
	trialUser, trialPass                              string
	deletedPeers                                      []string
	servers                                           []serverLine
	material                                          wireGuardMaterial
	addErr                                            error
}

func (f *fakeAPI) AddVPN(_ context.Context, user, _ string, _ int) (vpnAccount, error) {
	f.adds++
	f.addUsers = append(f.addUsers, user)
	if f.addErr != nil {
		return vpnAccount{}, f.addErr
	}
	return vpnAccount{Username: "sampleUsername", Password: "samplePassword"}, nil
}
func (f *fakeAPI) RenewVPN(_ context.Context, user string, _ int) error {
	f.renews++
	f.renewUsers = append(f.renewUsers, user)
	return nil
}
func (f *fakeAPI) DeleteVPN(context.Context, string, bool) error {
	f.deletes++
	return nil
}
func (f *fakeAPI) GetTrial(context.Context, string) (vpnAccount, error) {
	f.trials++
	user, pass := f.trialUser, f.trialPass
	if user == "" {
		user, pass = "sampleUsername", "samplePassword"
	}
	return vpnAccount{Username: user, Password: pass}, nil
}
func (f *fakeAPI) ListServers(context.Context) ([]serverLine, error) { return f.servers, nil }
func (f *fakeAPI) AddWireGuardPeer(context.Context, string, int64) (peerCreated, error) {
	f.peerAdds++
	return peerCreated{WireGuardID: "123", ClientIP: "10.0.0.2/32"}, nil
}
func (f *fakeAPI) GetWireGuardConfig(context.Context, string, string) (wireGuardMaterial, error) {
	return f.material, nil
}
func (f *fakeAPI) DeleteWireGuardPeer(_ context.Context, _, id string, _ bool) error {
	f.peerDels++
	f.deletedPeers = append(f.deletedPeers, id)
	return nil
}

type memStore struct {
	grants    []grant
	done      map[int64]string
	accounts  map[int64]account
	peers     []openPeer
	nextID    int64
	users     map[int64]string
	banned    map[int64]bool
	trials    map[int64]string
	sub       map[int64]time.Time
	subKnown  map[int64]bool
	saveFails int
}

func newMem() *memStore {
	return &memStore{
		done: map[int64]string{}, accounts: map[int64]account{}, users: map[int64]string{},
		banned: map[int64]bool{}, trials: map[int64]string{}, sub: map[int64]time.Time{},
		subKnown: map[int64]bool{},
	}
}

func (m *memStore) EnsureSyncAfter(context.Context, time.Time) (time.Time, error) {
	return time.Unix(0, 0).UTC(), nil
}
func (m *memStore) CollectOrders(context.Context, time.Time) error { return nil }
func (m *memStore) PendingGrants(context.Context, int) ([]grant, error) {
	var out []grant
	for _, g := range m.grants {
		if m.done[g.ID] == "" || m.done[g.ID] == "PENDING" {
			if m.done[g.ID] == "DONE" || m.done[g.ID] == "SKIPPED" {
				continue
			}
			out = append(out, g)
		}
	}
	return out, nil
}
func (m *memStore) FinishGrant(_ context.Context, id int64, status, _ string) error {
	m.done[id] = status
	return nil
}
func (m *memStore) RememberBilling(_ context.Context, id int64, username, password string) error {
	for i := range m.grants {
		if m.grants[i].ID == id {
			m.grants[i].Billed = true
			m.grants[i].UpstreamUser = username
			m.grants[i].UpstreamPass = password
			return nil
		}
	}
	return errors.New("missing grant")
}
func (m *memStore) AlignExpiry(_ context.Context, _ time.Time) error {
	for id, a := range m.accounts {
		if !m.subKnown[id] || a.Status != "ACTIVE" {
			continue
		}
		if sub, ok := m.sub[id]; ok && sub.Before(a.ExpireAt) {
			a.ExpireAt = sub
			m.accounts[id] = a
		}
	}
	return nil
}
func (m *memStore) UserAccountNo(_ context.Context, userID int64) (string, error) {
	if m.banned[userID] {
		return "", httpx.NewError(httpx.ErrAccountDisabled, "账户不可用")
	}
	return m.users[userID], nil
}
func (m *memStore) SubscriptionExpire(_ context.Context, userID int64) (time.Time, bool, error) {
	t, ok := m.sub[userID]
	return t, ok, nil
}
func (m *memStore) Account(_ context.Context, userID int64) (account, bool, error) {
	a, ok := m.accounts[userID]
	return a, ok, nil
}
func (m *memStore) SaveAccount(_ context.Context, a account) error {
	if m.saveFails > 0 {
		m.saveFails--
		return errors.New("db unavailable")
	}
	m.accounts[a.UserID] = a
	return nil
}
func (m *memStore) AccountsToDisable(_ context.Context, now time.Time) ([]account, error) {
	var out []account
	for _, a := range m.accounts {
		sub, known := m.sub[a.UserID]
		noSub := m.subKnown[a.UserID] && (!known || !sub.After(now))
		if a.Status == "ACTIVE" && (!a.ExpireAt.After(now) || m.banned[a.UserID] || noSub) {
			out = append(out, a)
		}
	}
	return out, nil
}
func (m *memStore) MarkDisabled(_ context.Context, userID int64) error {
	a := m.accounts[userID]
	a.Status = "DISABLED"
	m.accounts[userID] = a
	for i := range m.peers {
		if m.peers[i].UserID == userID {
			m.peers[i].ID = -m.peers[i].ID
		}
	}
	return nil
}
func (m *memStore) PeersToRelease(context.Context) ([]openPeer, error) {
	var out []openPeer
	for _, p := range m.peers {
		if p.ID > 0 && p.Releasing {
			out = append(out, p)
		}
	}
	return out, nil
}
func (m *memStore) MarkPeerReleased(_ context.Context, id int64) error {
	for i := range m.peers {
		if m.peers[i].ID == id {
			m.peers[i].ID = 0
		}
	}
	return nil
}
func (m *memStore) LineAccess(_ context.Context, userID int64, now time.Time, sandbox bool) (lineAccess, error) {
	a, ok := m.accounts[userID]
	if ok && a.Status == "ACTIVE" && a.ExpireAt.After(now) && a.Sandbox == sandbox {
		return lineAccess{Available: true}, nil
	}
	if ok {
		return lineAccess{Reason: "EXPIRED"}, nil
	}
	return lineAccess{Reason: "NO_PLAN"}, nil
}
func (m *memStore) DeviceOK(context.Context, int64, int64) error { return nil }
func (m *memStore) ActivePeers(_ context.Context, userID, except int64) (int, error) {
	n := 0
	for _, p := range m.peers {
		if p.ID > 0 && p.UserID == userID && p.DeviceID != except {
			n++
		}
	}
	return n, nil
}
func (m *memStore) DevicePeer(_ context.Context, userID, deviceID int64) (openPeer, bool, error) {
	for _, p := range m.peers {
		if p.ID > 0 && p.UserID == userID && p.DeviceID == deviceID {
			return p, true, nil
		}
	}
	return openPeer{}, false, nil
}
func (m *memStore) InsertPeer(_ context.Context, p openPeer) error {
	m.nextID++
	p.ID = m.nextID
	m.peers = append(m.peers, p)
	return nil
}
func (m *memStore) RequestRelease(ctx context.Context, userID, deviceID int64) (openPeer, bool, error) {
	p, ok, err := m.DevicePeer(ctx, userID, deviceID)
	if !ok || err != nil {
		return p, ok, err
	}
	for i := range m.peers {
		if m.peers[i].ID == p.ID {
			m.peers[i].Releasing = true
		}
	}
	p.Releasing = true
	return p, true, nil
}

func TestGrantThenRenewAndDisable(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	mem := newMem()
	mem.users[7] = "8302571644"
	mem.sub[7] = now.Add(30 * 24 * time.Hour)
	mem.grants = []grant{{ID: 1, UserID: 7, Months: 1}}
	api := &fakeAPI{}
	svc := &Service{Settings: Settings{Username: "u", Password: "p", Sandbox: true}, API: api, Store: mem, Clock: clockAt(now)}
	if err := svc.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if api.adds != 1 || api.renews != 0 || mem.accounts[7].Username != "yh8302571644" || mem.accounts[7].Sandbox != true {
		t.Fatalf("add: calls=%d renew=%d acc=%+v", api.adds, api.renews, mem.accounts[7])
	}
	if mem.accounts[7].Password == "samplePassword" || mem.accounts[7].Password == "" {
		t.Fatal("sandbox must keep a local password, not the documented dummy")
	}
	mem.grants = append(mem.grants, grant{ID: 2, UserID: 7, Months: 3})
	if err := svc.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if api.adds != 1 || api.renews != 1 {
		t.Fatalf("renew: adds=%d renews=%d", api.adds, api.renews)
	}
	mem.accounts[7] = account{UserID: 7, Username: "yh8302571644", Status: "ACTIVE", ExpireAt: now.Add(-time.Hour), Source: "ORDER", Sandbox: true}
	if err := svc.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if api.deletes != 1 || mem.accounts[7].Status != "DISABLED" {
		t.Fatalf("disable: deletes=%d status=%s", api.deletes, mem.accounts[7].Status)
	}
}

func TestConnectPeerLimitAndRelease(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	mem := newMem()
	mem.accounts[7] = account{UserID: 7, Username: "yh8302571644", Password: "local", Status: "ACTIVE", ExpireAt: now.Add(24 * time.Hour), Sandbox: true}
	for i := int64(1); i <= 3; i++ {
		mem.peers = append(mem.peers, openPeer{ID: i, UserID: 7, DeviceID: 100 + i, ServerID: 1, WireGuardID: "p", Sandbox: true})
	}
	api := &fakeAPI{material: wireGuardMaterial{
		PrivateKey: testKey(1), Address: "10.0.0.2/32", DNS: []string{"1.1.1.1"},
		ServerPublicKey: testKey(2), Endpoint: "vpn.example.com:51820", MTU: 1280,
	}}
	svc := &Service{Settings: Settings{Username: "u", Password: "p", Sandbox: true}, API: api, Store: mem, Clock: clockAt(now)}
	if _, err := svc.Connect(context.Background(), 7, 9, "w43"); codeOf(err) != httpx.ErrDeviceLimit {
		t.Fatalf("cap: %v", err)
	}
	if api.peerAdds != 0 {
		t.Fatal("must not add a fourth peer")
	}
	issued, err := svc.Connect(context.Background(), 7, 101, "w43")
	if err != nil {
		t.Fatal(err)
	}
	if issued.LineID != "w43" || issued.ServerID != "43" || issued.InterfacePrivateKey != testKey(1) || api.peerDels != 1 {
		t.Fatalf("%+v dels=%d", issued, api.peerDels)
	}
	if err := svc.Release(context.Background(), 7, 101); err != nil {
		t.Fatal(err)
	}
	if api.peerDels != 2 {
		t.Fatalf("release dels=%d", api.peerDels)
	}
}

func TestLinesAndTrial(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	mem := newMem()
	api := &fakeAPI{servers: []serverLine{{ID: 43, Name: "Australia S1", IP: "203.0.113.10"}}}
	svc := &Service{Settings: Settings{Username: "u", Password: "p", Sandbox: true}, API: api, Store: mem, Clock: clockAt(now)}
	lines, err := svc.Lines(context.Background(), 7)
	if err != nil || len(lines) != 1 || lines[0].Available || lines[0].LockedReason == nil || *lines[0].LockedReason != "NO_PLAN" || lines[0].Probe != nil {
		t.Fatalf("%+v %v", lines, err)
	}
	if lines[0].CountryCode != "AU" || lines[0].ID != "w43" {
		t.Fatalf("place: %+v", lines[0])
	}
	mem.users[7] = "8302571644"
	mem.grants = []grant{{ID: 1, UserID: 7, Months: 0, Source: "TRIAL"}}
	if err := svc.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if api.trials != 1 || mem.accounts[7].Source != "TRIAL" || !mem.accounts[7].ExpireAt.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("trial %+v calls=%d", mem.accounts[7], api.trials)
	}
	lines, err = svc.Lines(context.Background(), 7)
	if err != nil || !lines[0].Available || lines[0].Probe == nil || lines[0].Probe.Host != "203.0.113.10" || lines[0].Probe.Port != ProbePort {
		t.Fatalf("available: %+v %v", lines, err)
	}
	if err := svc.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if api.trials != 1 {
		t.Fatalf("second sync must not open another trial, calls=%d", api.trials)
	}
}

func TestTrialThenPaidRenewsRealWholesaleUser(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	mem := newMem()
	mem.users[7] = "8302571644"
	mem.sub[7] = now.Add(24 * time.Hour)
	mem.grants = []grant{{ID: 1, UserID: 7, Months: 0, Source: "TRIAL"}}
	api := &fakeAPI{trialUser: "wvpn-issued-7", trialPass: "issued-secret"}
	svc := &Service{Settings: Settings{Username: "reseller", Password: "secret", Sandbox: false, LiveConfirm: true}, API: api, Store: mem, Clock: clockAt(now)}
	if err := svc.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if mem.accounts[7].Username != "wvpn-issued-7" || mem.accounts[7].Password != "issued-secret" {
		t.Fatalf("trial stored local name: %+v", mem.accounts[7])
	}
	if mem.accounts[7].Username == "yh8302571644" {
		t.Fatal("stored the local placeholder")
	}
	mem.sub[7] = now.Add(30 * 24 * time.Hour)
	mem.grants = append(mem.grants, grant{ID: 2, UserID: 7, Months: 1, Source: "ORDER"})
	if err := svc.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if api.adds != 0 || api.renews != 1 || len(api.renewUsers) != 1 || api.renewUsers[0] != "wvpn-issued-7" {
		t.Fatalf("paid after trial: adds=%d renews=%v", api.adds, api.renewUsers)
	}
	if mem.done[2] != "DONE" || mem.accounts[7].Username != "wvpn-issued-7" {
		t.Fatalf("grant not finished on the issued account: %+v done=%s", mem.accounts[7], mem.done[2])
	}
}

func TestBillingIsNotRepeatedWhenSaveFails(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	mem := newMem()
	mem.users[7] = "8302571644"
	mem.sub[7] = now.Add(30 * 24 * time.Hour)
	mem.grants = []grant{{ID: 1, UserID: 7, Months: 1, Source: "ORDER"}}
	mem.saveFails = 1
	api := &fakeAPI{}
	svc := &Service{Settings: Settings{Username: "reseller", Password: "secret", Sandbox: true}, API: api, Store: mem, Clock: clockAt(now)}
	if err := svc.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if api.adds != 1 || api.renews != 0 || mem.done[1] == "DONE" {
		t.Fatalf("first attempt adds=%d renews=%d done=%s", api.adds, api.renews, mem.done[1])
	}
	if !mem.grants[0].Billed || mem.grants[0].UpstreamUser != "yh8302571644" {
		t.Fatalf("billing was not remembered: %+v", mem.grants[0])
	}
	if err := svc.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if api.adds != 1 || api.renews != 0 || mem.done[1] != "DONE" || mem.accounts[7].Username != "yh8302571644" {
		t.Fatalf("retry billed again: adds=%d renews=%d done=%s acc=%+v", api.adds, api.renews, mem.done[1], mem.accounts[7])
	}
}

func TestAlreadyExistsDoesNotRenew(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	mem := newMem()
	mem.users[7] = "8302571644"
	mem.sub[7] = now.Add(30 * 24 * time.Hour)
	mem.grants = []grant{{ID: 1, UserID: 7, Months: 1, Source: "ORDER"}}
	api := &fakeAPI{addErr: &APIError{Action: "addVPN", Message: "user already exists"}}
	svc := &Service{Settings: Settings{Username: "reseller", Password: "secret", Sandbox: true}, API: api, Store: mem, Clock: clockAt(now)}
	if err := svc.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if api.adds != 1 || api.renews != 0 || mem.done[1] != "DONE" {
		t.Fatalf("exists path billed a renewal: adds=%d renews=%d done=%s", api.adds, api.renews, mem.done[1])
	}
}

func TestRevokedDeviceReleasesWholesalePeer(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	mem := newMem()
	mem.users[7] = "8302571644"
	mem.sub[7] = now.Add(30 * 24 * time.Hour)
	mem.accounts[7] = account{UserID: 7, Username: "wvpn-issued-7", Password: "pw", Status: "ACTIVE", ExpireAt: mem.sub[7], Sandbox: true}
	mem.peers = []openPeer{{ID: 4, UserID: 7, DeviceID: 9, ServerID: 43, WireGuardID: "wg-9", Sandbox: true}}
	api := &fakeAPI{}
	svc := &Service{Settings: Settings{Username: "reseller", Password: "secret", Sandbox: true}, API: api, Store: mem, Clock: clockAt(now)}
	if _, ok, err := mem.RequestRelease(context.Background(), 7, 9); err != nil || !ok || !mem.peers[0].Releasing {
		t.Fatalf("revoke did not mark peer: %+v %v", mem.peers, err)
	}
	if err := svc.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if api.peerDels != 1 || len(api.deletedPeers) != 1 || api.deletedPeers[0] != "wg-9" {
		t.Fatalf("peer not deleted: %+v", api.deletedPeers)
	}
}

func TestShortenedSubscriptionDisablesWholesaleAccount(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	mem := newMem()
	mem.users[7] = "8302571644"
	mem.subKnown[7] = true
	mem.sub[7] = now.Add(-time.Hour)
	mem.accounts[7] = account{UserID: 7, Username: "wvpn-issued-7", Password: "pw", Status: "ACTIVE", ExpireAt: now.Add(20 * 24 * time.Hour), Source: "ORDER", Sandbox: true}
	api := &fakeAPI{}
	svc := &Service{Settings: Settings{Username: "reseller", Password: "secret", Sandbox: true}, API: api, Store: mem, Clock: clockAt(now)}
	if err := svc.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if api.deletes != 1 || mem.accounts[7].Status != "DISABLED" {
		t.Fatalf("shortened subscription left wholesale active: deletes=%d %+v", api.deletes, mem.accounts[7])
	}
}

func TestShortenedButStillValidSubscriptionPullsExpiryForward(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	mem := newMem()
	mem.users[7] = "8302571644"
	mem.subKnown[7] = true
	mem.sub[7] = now.Add(48 * time.Hour)
	mem.accounts[7] = account{UserID: 7, Username: "wvpn-issued-7", Password: "pw", Status: "ACTIVE", ExpireAt: now.Add(30 * 24 * time.Hour), Source: "ORDER", Sandbox: true}
	api := &fakeAPI{}
	svc := &Service{Settings: Settings{Username: "reseller", Password: "secret", Sandbox: true}, API: api, Store: mem, Clock: clockAt(now)}
	if err := svc.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if api.deletes != 0 || !mem.accounts[7].ExpireAt.Equal(mem.sub[7]) || mem.accounts[7].Status != "ACTIVE" {
		t.Fatalf("expiry not aligned: deletes=%d %+v", api.deletes, mem.accounts[7])
	}
}

func codeOf(err error) httpx.Code {
	var e *httpx.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return 0
}

type fixedClock time.Time

func (c fixedClock) Now() time.Time { return time.Time(c) }

func clockAt(t time.Time) clock.Clock { return fixedClock(t) }
