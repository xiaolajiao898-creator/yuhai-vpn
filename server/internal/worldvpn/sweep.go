package worldvpn

import "context"

// SweepHit 是一次按用户名探测到的上游 WireGuard 配置编号。不包含配置正文。
type SweepHit struct {
	ID      string
	Deleted bool
}

type configSweeper interface {
	GetWireGuardConfig(ctx context.Context, username, wireguardID string) (wireGuardMaterial, error)
	DeleteWireGuardPeer(ctx context.Context, username, wireguardID string, sandbox bool) error
}

// SweepConfigs 用 getWireGuardConfig 按编号探测某个 vpnUsername 名下的配置。
// 这个接口按用户名隔离，探测到的编号只属于该用户。doDelete 为 false 时只列出。
func SweepConfigs(ctx context.Context, api configSweeper, username string, from, to int, doDelete, sandbox bool) ([]SweepHit, error) {
	if username == "" || from < 1 || to < from || to-from > 100 {
		return nil, &APIError{Action: "wvsweep", Message: "探测范围无效"}
	}
	var hits []SweepHit
	for n := from; n <= to; n++ {
		id := itoa(int64(n))
		if _, err := api.GetWireGuardConfig(ctx, username, id); err != nil {
			continue
		}
		hit := SweepHit{ID: id}
		if doDelete {
			if err := api.DeleteWireGuardPeer(ctx, username, id, sandbox); err != nil && !alreadyGone(err) {
				hits = append(hits, hit)
				return hits, err
			}
			hit.Deleted = true
		}
		hits = append(hits, hit)
	}
	return hits, nil
}
