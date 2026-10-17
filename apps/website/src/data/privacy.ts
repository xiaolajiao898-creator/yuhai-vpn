// 官网「我们收集什么」清单。每一行都要能在代码或运维配置里找到依据（依据写在 basis 里，不对外显示）。
// 规则变化时先改这里再上线；没有落地的保留期写「尚未设定」，不编造天数。
export type PrivacyRow = { item: string; purpose: string; retention: string; basis: string };

export const privacyData: PrivacyRow[] = [
  {
    item: '你的 USDT（TRON）地址',
    purpose: '作为登录账号，以及找回密码时核对链上转账',
    retention: '账户存续期间',
    basis: 'migrations/00040：只存 HMAC 查找键与加密副本（login_lookup / login_address_enc）',
  },
  {
    item: '登录密码',
    purpose: '登录验证；只保存不可逆的 Argon2id 哈希，我们也看不到原文',
    retention: '账户存续期间',
    basis: 'internal/account/password.go（argon2id）',
  },
  {
    item: '设备记录（系统类型、型号、最近活跃时间、设备公钥）',
    purpose: '限制同时使用的设备数、校验请求来自你的设备、让你随时移除设备',
    retention: '设备在账户中期间；移除后的记录保留期尚未设定',
    basis: 'devices / device_credentials 表；设备移除接口',
  },
  {
    item: '注册与绑定设备时的 IP（只存哈希）',
    purpose: '防止批量注册等滥用',
    retention: '30 天后自动清除',
    basis: 'internal/account/jobs.go PurgeIPHashes（30 天置空）',
  },
  {
    item: '登录会话',
    purpose: '保持你的登录状态',
    retention: '会话过期 90 天后自动删除',
    basis: 'internal/account/jobs.go PurgeExpiredSessions',
  },
  {
    item: '订单与链上付款记录',
    purpose: '开通套餐、到账核对与财务对账',
    retention: '长期保存（财务对账需要）',
    basis: 'orders / chain_transfers / 资金流水表',
  },
  {
    item: '每日流量字节数（按设备、线路汇总，不含访问目标）',
    purpose: '计算套餐流量用量',
    retention: '自动清理规则尚未设定，设定后在此公布',
    basis: 'traffic_daily；目前无清理任务',
  },
];
