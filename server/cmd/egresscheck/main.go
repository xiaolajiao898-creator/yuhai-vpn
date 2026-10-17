// egresscheck 是出口配置的本地核对工具：从标准输入读一条出口（与后台导入同一份解析实现），
// 输出客户端要交给 Xray 的完整配置。只用于隔离测试与排障，输出含凭据，不要贴到任何地方。
//
//	echo 'trojan://...' | egresscheck -port 10808 -proxy-private > client.json
//	echo 'trojan://...' | egresscheck -tun > tun.json    # 与下发给安卓的形态一致
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/yuhai-vpn/server/internal/egress"
)

func main() {
	port := flag.Int("port", 10808, "测试模式下本机 SOCKS 监听端口（仅 127.0.0.1）")
	tun := flag.Bool("tun", false, "输出 TUN 模式配置（与服务端下发一致）")
	private := flag.Bool("proxy-private", false, "允许本机/内网出口，且私有地址也走出口（仅测试模式，靶机在本机时使用）")
	udp := flag.Bool("udp", false, "无协议信息的行默认视为支持 UDP")
	proto := flag.String("default-protocol", egress.SOCKS5, "无协议前缀的行按此协议解析")
	flag.Parse()

	raw, err := io.ReadAll(bufio.NewReader(io.LimitReader(os.Stdin, 64<<10)))
	if err != nil {
		fail("读取输入失败")
	}
	rows, err := egress.Parse(strings.TrimSpace(string(raw)), egress.ParseOptions{DefaultProtocol: *proto, DefaultUDP: *udp, AllowPrivate: *private})
	if err != nil {
		fail(err.Error())
	}
	if len(rows) != 1 {
		fail(fmt.Sprintf("需要恰好一条出口，读到 %d 条", len(rows)))
	}
	if rows[0].Error != "" || rows[0].Endpoint == nil {
		fail("第 1 行：" + rows[0].Error)
	}
	o := egress.ClientOptions{Mode: egress.ModeSOCKS, SocksPort: *port, ProxyPrivate: *private, LogLevel: "warning"}
	if *tun {
		if *private {
			fail("-tun 与 -proxy-private 不能同时使用")
		}
		o = egress.ClientOptions{Mode: egress.ModeTUN}
	}
	cfg, err := rows[0].Endpoint.ClientConfig(o)
	if err != nil {
		fail(err.Error())
	}
	fmt.Fprintf(os.Stderr, "协议=%s 身份=%s UDP=%v 内核=Xray %s\n", rows[0].Endpoint.Protocol,
		rows[0].Endpoint.MaskedIdentity(), rows[0].Endpoint.SupportsUDP, egress.XrayCoreVersion)
	os.Stdout.Write(cfg)
	os.Stdout.Write([]byte("\n"))
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "egresscheck:", msg)
	os.Exit(2)
}
