// wgcheck 在真实节点上核对 `wg` 的实际行为是否与本项目的解析和下发假设一致。
//
// 默认只读：只执行 `wg show`，不改变任何状态。
// 加 -write 才会在指定接口上添加并随即移除一个一次性测试 peer，用于验证 SetPeers
// 拼出的 argv 能被真实 wg 接受。**不要在承载真实用户的接口上加 -write。**
//
//	go run ./cmd/wgcheck -wg /usr/bin/wg -iface wg0
//	go run ./cmd/wgcheck -wg /usr/bin/wg -iface wgtest -write
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/yuhai-vpn/server/internal/nodeagent"
	"github.com/yuhai-vpn/server/internal/nodewire"
)

var problems []string

func fail(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	problems = append(problems, msg)
	fmt.Printf("  ✗ %s\n", msg)
}

func ok(format string, args ...any) { fmt.Printf("  ✓ %s\n", fmt.Sprintf(format, args...)) }

func note(format string, args ...any) { fmt.Printf("  · %s\n", fmt.Sprintf(format, args...)) }

func main() {
	binary := flag.String("wg", "/usr/bin/wg", "wg 可执行文件的绝对路径")
	iface := flag.String("iface", "wg0", "WireGuard 接口名")
	write := flag.Bool("write", false, "额外做一次 peer 增删往返（会改变该接口状态）")
	flag.Parse()

	cmd := &nodeagent.WGCommand{Binary: *binary}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fmt.Printf("核对 %s 在接口 %s 上的行为\n\n", *binary, *iface)

	fmt.Println("[1] wg show <iface> public-key")
	pub := checkPublicKey(ctx, cmd, *iface)

	fmt.Println("[2] wg show <iface> listen-port")
	checkListenPort(ctx, cmd, *iface)

	fmt.Println("[3] wg show <iface> allowed-ips")
	peers := checkAllowedIPs(ctx, cmd, *iface, pub)

	fmt.Println("[4] wg show <iface> latest-handshakes")
	checkHandshakes(ctx, cmd, *iface, peers)

	if *write {
		fmt.Println("[5] SetPeers 增删往返（会改变接口状态）")
		checkRoundTrip(ctx, cmd, *iface)
	} else {
		fmt.Println("[5] SetPeers 增删往返：已跳过（加 -write 才执行）")
	}

	fmt.Println()
	if len(problems) == 0 {
		fmt.Println("全部假设与真实 wg 行为一致。")
		return
	}
	fmt.Printf("发现 %d 处不一致：\n", len(problems))
	for _, p := range problems {
		fmt.Printf("  - %s\n", p)
	}
	os.Exit(1)
}

func checkPublicKey(ctx context.Context, cmd *nodeagent.WGCommand, iface string) string {
	raw, err := cmd.Show(ctx, iface, "public-key")
	if err != nil {
		fail("读取失败：%v（接口是否存在、是否有权限？）", err)
		return ""
	}
	if !strings.HasSuffix(string(raw), "\n") {
		fail("输出未以换行结尾，业务代码按 %q 比对会失配", "值+\\n")
	}
	value := strings.TrimRight(string(raw), "\n")
	if strings.ContainsAny(value, " \t\n") {
		fail("输出含空白字符：%q", value)
	}
	if !nodewire.ValidWGPublicKey(value) {
		fail("公钥未通过本项目的校验：%q", value)
	} else {
		ok("单行、以换行结尾、通过公钥校验")
	}
	return value
}

func checkListenPort(ctx context.Context, cmd *nodeagent.WGCommand, iface string) {
	raw, err := cmd.Show(ctx, iface, "listen-port")
	if err != nil {
		fail("读取失败：%v", err)
		return
	}
	value := strings.TrimRight(string(raw), "\n")
	port, err := strconv.Atoi(value)
	if err != nil {
		fail("端口不是十进制整数：%q", value)
		return
	}
	if port == 0 {
		fail("端口报 0。业务代码会把它与清单里的 listen_port 比对并在不符时拒绝，"+
			"这会让该节点静默地永久不就绪、永久不下发 peer（当前值 %q）。\n"+
			"      已知成因之一：wireguard-go（用户态实现）在**主机禁用 IPv6** 时，"+
			"conn/bind_std.go 的 Open() 会被失败的 udp6 监听把 port 覆写为 0，"+
			"此时 IPv4 其实已正常绑定，但 UAPI 不再上报 listen_port。\n"+
			"      请依次确认：接口已 up；主机 IPv6 是否禁用；以及该节点用的是内核模块还是 wireguard-go。", value)
		return
	}
	if port < 1 || port > 65535 {
		fail("端口越界：%d", port)
		return
	}
	ok("端口为 %d，可与清单 listen_port 直接比对", port)
}

func checkAllowedIPs(ctx context.Context, cmd *nodeagent.WGCommand, iface, selfKey string) []string {
	before := len(problems)
	raw, err := cmd.Show(ctx, iface, "allowed-ips")
	if err != nil {
		fail("读取失败：%v", err)
		return nil
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		note("接口当前没有 peer，本项无法核对分隔符；请在有 peer 的接口上重跑")
		return nil
	}
	if strings.Contains(string(raw), ",") {
		fail("多个 allowed-ips 之间出现逗号。本项目用空白切分，逗号会让 IP 变成 "+
			"\"10.0.0.1/32,\" 这样的无效串，导致状态比对恒不通过。原始输出：%q", string(raw))
	}
	rows, err := nodeagent.ParseWGRows(raw, 1000)
	if err != nil {
		fail("本项目的切分逻辑无法解析该输出：%v", err)
		return nil
	}
	var keys []string
	for i, row := range rows {
		if len(row) < 2 || len(row) > 3 {
			fail("第 %d 行字段数为 %d，业务代码只接受 2~3（公钥 + 1~2 个 IP）：%v", i+1, len(row), row)
			continue
		}
		if !nodewire.ValidWGPublicKey(row[0]) {
			fail("第 %d 行首字段不是合法公钥：%q", i+1, row[0])
			continue
		}
		if row[0] == selfKey {
			fail("peer 列表里出现了接口自身的公钥，业务代码会拒绝处理")
		}
		for _, ip := range row[1:] {
			if !nodewire.ValidWGPeerIP(ip) {
				fail("第 %d 行的 %q 未通过本项目的 IP 校验（要求私网、主机前缀、规范写法）", i+1, ip)
			}
		}
		keys = append(keys, row[0])
	}
	if len(problems) == before {
		ok("%d 个 peer，制表符分隔公钥、空格分隔多个 IP，全部通过校验", len(rows))
	}
	return keys
}

func checkHandshakes(ctx context.Context, cmd *nodeagent.WGCommand, iface string, peers []string) {
	before := len(problems)
	raw, err := cmd.Show(ctx, iface, "latest-handshakes")
	if err != nil {
		fail("读取失败：%v", err)
		return
	}
	if len(peers) == 0 {
		note("无 peer，跳过")
		return
	}
	rows, err := nodeagent.ParseWGRows(raw, 1000)
	if err != nil {
		fail("无法解析：%v", err)
		return
	}
	if len(rows) != len(peers) {
		fail("握手行数 %d 与 peer 数 %d 不一致，observer 会据此判定不就绪", len(rows), len(peers))
		return
	}
	now := time.Now().Unix()
	for i, row := range rows {
		if len(row) != 2 {
			fail("第 %d 行字段数为 %d，observer 只接受 2", i+1, len(row))
			continue
		}
		ts, err := strconv.ParseInt(row[1], 10, 64)
		if err != nil || ts < 0 {
			fail("第 %d 行时间戳非法：%q", i+1, row[1])
			continue
		}
		if ts > now {
			fail("第 %d 行时间戳在未来（%d > %d），observer 会判定不就绪", i+1, ts, now)
		}
	}
	if len(problems) == before {
		ok("%d 行，均为「公钥 + Unix 秒」，未来时间戳检查通过", len(rows))
	}
}

func checkRoundTrip(ctx context.Context, cmd *nodeagent.WGCommand, iface string) {
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		fail("生成测试公钥失败：%v", err)
		return
	}
	seed[0] |= 1 // 避免全零公钥
	key := base64.StdEncoding.EncodeToString(seed)
	const testIP = "10.255.255.254/32"

	add := []nodeagent.WGPeerChange{{PublicKey: key, AllowedIPs: []string{testIP}}}
	if err := cmd.SetPeers(ctx, iface, add); err != nil {
		fail("真实 wg 拒绝了本项目拼出的新增 argv：%v", err)
		return
	}
	ok("新增 peer 被真实 wg 接受")

	raw, err := cmd.Show(ctx, iface, "allowed-ips")
	if err != nil || !strings.Contains(string(raw), key) {
		fail("新增后在 wg show 里找不到该 peer，下发后的收敛复核会失败")
	} else {
		ok("新增后可在 wg show 中读回")
	}

	remove := []nodeagent.WGPeerChange{{PublicKey: key, Remove: true}}
	if err := cmd.SetPeers(ctx, iface, remove); err != nil {
		fail("真实 wg 拒绝了本项目拼出的删除 argv：%v（测试 peer %s 可能残留，请手工清理）", err, key)
		return
	}
	raw, err = cmd.Show(ctx, iface, "allowed-ips")
	if err != nil || strings.Contains(string(raw), key) {
		fail("删除后该 peer 仍在，测试 peer %s 残留，请手工清理", key)
		return
	}
	ok("删除被接受且已生效，接口已恢复原状")
}
