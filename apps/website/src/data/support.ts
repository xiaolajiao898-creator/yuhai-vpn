// 官网「支持范围」表。只写已发布、已验证的；测试中的如实标注，不提前宣传。
// 变更前核对 deploy/MULTIPROTOCOL-WORKLOG.md 的协议 × 平台矩阵与候选版验证记录。
export type SupportRow = {
  platform: string;
  state: 'live' | 'test' | 'none';
  stateLabel: string;
  protocols: string;
  note: string;
};

export const support: SupportRow[] = [
  {
    platform: '安卓',
    state: 'live',
    stateLabel: '正式版',
    protocols: 'WireGuard',
    note: 'Android 8.0 及以上 · 官方签名安装包',
  },
  {
    platform: '安卓 · 更多协议',
    state: 'test',
    stateLabel: '内部测试',
    protocols: 'SOCKS5、HTTP/HTTPS 代理、Shadowsocks、Trojan、VLESS、VMess、Hysteria2',
    note: '内部测试中，尚未对外提供；HTTP 类代理不支持 UDP',
  },
  {
    platform: 'iOS',
    state: 'none',
    stateLabel: '准备中',
    protocols: '—',
    note: '上线后在下载页更新',
  },
  {
    platform: 'Windows / macOS',
    state: 'none',
    stateLabel: '未发布',
    protocols: '—',
    note: '上线后在下载页更新',
  },
];
