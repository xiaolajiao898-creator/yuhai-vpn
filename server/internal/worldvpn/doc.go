// Package worldvpn 把 WorldVPN 批发 API 接进宇海现有账号和 WireGuard 通道。
//
// 用户仍然登录宇海。已有的付款、兑换码（00042）和 24 小时试用（00043/00044）照旧开通本地权益；
// worker 再按这些结果去批发侧开或续账号。本包不创建、不修改 redeem_codes。
// 选线时线路来自 listServers；连接时由本服务 addWireGuardPeer + getWireGuardConfig，
// 再把解析后的配置交给安卓 / Windows 现有引擎。用户不安装 WorldVPN 自己的 App。
//
// 批发登录只读环境变量 WORLDVPN_RESELLER_USERNAME / WORLDVPN_RESELLER_PASSWORD。
// WORLDVPN_SANDBOX 默认 true（请求带 mode=test，不扣点、不在真实节点建账号）。
// 沙箱里创建的账号不会在关闭沙箱后被当成真实账号续费。
//
// 三条 WireGuard 的含义见 peer_limit.go。本服务按「每个宇海用户（一个 vpnUsername）
// 同时最多 3 条」执行，并在断开时删除 peer。
package worldvpn
