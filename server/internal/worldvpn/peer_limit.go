package worldvpn

// MaxActivePeersPerUser 是本服务对「一个宇海用户 = 一个 WorldVPN vpnUsername」
// 同时持有的 WireGuard 配置条数上限，和套餐里的 3 台设备对齐。
//
// 官网有两套说法，核对结论如下（2026-11-07 读到的页面）：
//
//   - 批发定价（/pricing）：1 Pin = 1 个客户 1 个月，并且 includes 3 devices；
//     100 Pin 到 2000 Pin 每一档都写着同样的 “3 Simultaneous Connections”。
//     若这 3 条是整个批发登录全局共享，买得越多也不能让第 4 个客户同时上线，
//     和「每个客户含 3 台设备」矛盾。因此按每个 VPN 用户 3 条理解。
//   - API 说明（/worldvpn-api）的 FAQ 写的是 “limits are linked to the reseller
//     account type” 以及 “Reseller accounts support up to 3 active WireGuard
//     configurations simultaneously”。单看这句会像全局只有 3 条。它出现在
//     「给某个 VPN 用户创建配置」的段落里，和定价页放在一起时，更像是批发产品
//     给每个终端账号的上限，而不是整个批发账号一把锁死 3 条。
//   - 旧文案（vpnkeys.com 镜像）写的是按用户套餐：budget 3 条、unlimited 5 条，
//     也是按 VPN 用户而不是按批发登录全局计数。
//
// 2026-11-13 实测：试用（dialup）账号上游拒绝第 2 条，原文是
// "maximum limit of 1 WireGuard configurations for your account type (dialup)"。
// 试用在连接时按 1 条执行。付费按订阅里的 device_limit，读不到时用这里的 3。
const MaxActivePeersPerUser = 3
