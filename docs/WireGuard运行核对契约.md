# WireGuard 运行核对（第十七轮开发契约）

本模块只核对已配置的 Linux WireGuard，不安装或启动隧道、不增删 peer、不修改网络。核对结果用于候选节点选择，不等于端到端通信或计费已验证。只有显式启用 `NODE_WG_OBSERVER_ENABLED=true` 才运行独立的 `node-runtime` 进程。

## 输入与可信边界

输入是仅含公开材料的私有 JSON 清单，由后续控制面配置同步模块产生。当前配置同步尚未完成；不能把手工填写清单当成已从控制面同步。文件必须绝对路径、普通非符号链接、当前用户所有且权限0600，最多1MiB。全部字段必填，拒绝重复、未知和null。

清单 schema=1，含 node_id（正int64字符串）、config_version（正int64字符串）、interface（1–15位字母数字下划线短横点，禁止lo/all/interfaces）、public_key（非零32字节标准Base64）、listen_port（1–65535）、peers（最多1000条）。每条 peer 只有 credential_id（正int64字符串）、public_key、allowed_ips（1–2条规范的私有IPv4 /32 或IPv6 ULA /128主机地址）。公钥、凭证和地址均不可重复；拒绝节点公钥出现在peer中，拒绝子网/默认路由。空peer列表是有效的已核对空快照，和读取失败不同。

checksum 为仅包含 credential_id/public_key/allowed_ips 的公开 peer 投影：按 credential_id 整数升序，每条 allowed_ips 字符串升序，以Go JSON紧凑编码（无末尾换行）后的 SHA256。它是本地 WireGuard 投影校验，不冒充尚未实现的完整控制面 `/users/snapshot` 校验。

## 只读核对

- 只允许固定 argv 的 `wg show <interface> public-key|listen-port|allowed-ips|latest-handshakes`；不使用 shell，不运行 dump/showconf/private-key/preshared-keys，不记录命令输出。
- 配置指定绝对路径 wg 二进制，要求普通可执行文件、root或当前用户所有、不可被组或其他人写；固定最少环境变量，关闭stdin，丢弃stderr。单次2秒，总核对5秒，stdout最多256KiB；失败统一错误，不泄露公钥/地址。
- 核对网卡存在且UP、服务器公钥/端口、完整peer公钥及允许地址集合。采样前后重复读公开配置和网卡索引，清单文件前后字节必须相同；任何增删/漂移/重复/缺字段/超量/超时均不发布新状态。核对属于有界采样，不能消除采样后外部进程立即改配置的竞争。
- online_count 是最近180秒内成功握手的 peer 数，未来握手时间拒绝。零握手不算活跃；此值是活跃peer估计，不是TCP连接数、准确在线人数或计费依据。用户空闲时WireGuard可不发送数据，因此不得据此认定用户已退出。

## 输出与失效

输出为第2版 RuntimeStatus（node_id、observed_at、config_version、snapshot_checksum、online_count、ready_protocols）。当前观察器只发布 ready_protocols=[WIREGUARD]。状态放在单独0700目录的runtime.json，独占锁防同目录双开，以0600临时文件原子替换并同步磁盘。普通启动必须使用已存在的安全目录；`init-state`仅创建新目录。

每3秒核对一次。读取/核对失败立即删去本观察器的runtime.json；无法失效则停止。进程停止后状态在10秒内自然过期。状态只在完整核对后发布，空peer成功核对才允许online_count=0。下游心跳的持久重试策略仍保留，不伪造新的在线记录。

心跳新增可选ready_protocols（缺省空，最多5个不同规范协议）。App只暴露配置启用且本次心跳报告就绪的协议交集；空交集为准备中。历史回执迁移后为空，不能自动继承所有协议已就绪。

官方依据：[wg工具文档](https://git.zx2c4.com/wireguard-tools/about/src/man/wg.8)、[WireGuard快速开始](https://www.wireguard.com/quickstart/)。真实Linux/WireGuard及原生端到端测试仍需独立验收；本模块没有实际配置分发/撤销/计费能力。

## 候选启动接线

先完成独立审查，再在测试节点安装官方WireGuard工具和配置现有接口。此程序不安装VPN，也不接受默认路由或网络配置命令。目录用同一专用节点用户，所需只读wg调用在Linux通常仍需CAP_NET_ADMIN；该能力实际足以修改网络，代码的只读argv约束不能替代操作系统权限隔离。

环境变量：NODE_WG_NODE_ID、NODE_WG_BINARY（例如/usr/bin/wg）、NODE_WG_MANIFEST_FILE、NODE_WG_RUNTIME_DIR都必须明确配置。私有清单必须反映实际授权peer；不提供含假公钥的可直接启用配置。首次只执行node-runtime init-state建立新目录；随后once核对一次，无参数常驻。node-agent的NODE_RUNTIME_STATUS_FILE指向该目录/runtime.json，节点ID必须与mTLS证书一致。两个程序使用不同状态目录，不能共享或复制锁。

核对失败撤下runtime.json；本地状态至多10秒有效，控制面上次心跳仍有90秒候选窗口，App节点页每30秒重新读取。既有持久待发心跳仍可按原内容核实，因此并不承诺故障立刻传播，更不代表已撤销既有隧道。撤销同步和主动探测是后续独立工作。
