// wvsweep 按 vpnUsername 探测并可选删除上游残留的 WireGuard 配置。
//
// 默认只探测，不删除。getWireGuardConfig 按用户名隔离，探测到的编号只属于这个用户。
// 不打印配置正文、口令或地址。
//
//	go run ./cmd/wvsweep -username <vpnUsername>
//	go run ./cmd/wvsweep -username <vpnUsername> -from 1 -to 20 -delete
//
// 凭据来自环境变量 WORLDVPN_RESELLER_USERNAME / WORLDVPN_RESELLER_PASSWORD。
// 真实上游还要 WORLDVPN_SANDBOX=false 和 WORLDVPN_LIVE_CONFIRM=yes。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/yuhai-vpn/server/internal/worldvpn"
)

func main() {
	username := flag.String("username", "", "要清理的上游 vpnUsername")
	from := flag.Int("from", 1, "起始 config_id（含）")
	to := flag.Int("to", 20, "结束 config_id（含）")
	doDelete := flag.Bool("delete", false, "删除探测到的配置；默认只列出")
	flag.Parse()

	if *username == "" {
		fmt.Fprintln(os.Stderr, "usage: wvsweep -username <vpnUsername> [-from 1] [-to 20] [-delete]")
		os.Exit(2)
	}
	settings, err := worldvpn.LoadSettings()
	if err != nil {
		fmt.Fprintln(os.Stderr, "批发配置无效")
		os.Exit(1)
	}
	if !settings.Enabled() {
		fmt.Fprintln(os.Stderr, "批发客户端未启用")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	hits, err := worldvpn.SweepConfigs(ctx, worldvpn.NewClient(settings), *username, *from, *to, *doDelete, settings.Sandbox)
	if !*doDelete {
		fmt.Println("dry-run: pass -delete to remove")
	}
	for _, hit := range hits {
		if hit.Deleted {
			fmt.Printf("deleted config_id %s\n", hit.ID)
			continue
		}
		fmt.Printf("config_id %s\n", hit.ID)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sweep stopped")
		os.Exit(1)
	}
}
