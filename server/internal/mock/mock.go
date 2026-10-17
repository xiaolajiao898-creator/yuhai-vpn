// Package mock 汇集阶段一的“模拟”能力：模拟到账、模拟流量、可拨动时钟。
//
// 铁律（方案 6.9）：模拟到账不走独立代码路径，而是构造一条 network='MOCK' 的 chain_transfers 记录，
// 然后调用与生产完全相同的到账事务（deposit.Settle）。这样阶段一验证的就是阶段二的真实逻辑。
// 生产环境启动时若存在 MOCK 网络的收款地址或 DEV_TOOLS_ENABLED=true，config.Load 已拒绝启动；
// 路由层再做一次保护：仅当 !cfg.IsProduction() && cfg.DevToolsEnabled 时注册 /admin/v1/dev/*。
package mock

const NetworkMock = "MOCK"
