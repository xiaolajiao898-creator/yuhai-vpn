package nodeagent

import "github.com/yuhai-vpn/server/internal/nodewire"

type WGManifest = nodewire.WGManifest
type WGPeer = nodewire.WGPeer

func validWGInterface(s string) bool { return nodewire.ValidWGInterface(s) }
func parseWGManifest(raw []byte, nodeID string) (WGManifest, string, error) {
	return nodewire.ParseWGManifest(raw, nodeID)
}

func validPublicKey(s string) bool { return nodewire.ValidWGPublicKey(s) }
func validPeerIP(s string) bool    { return nodewire.ValidWGPeerIP(s) }
