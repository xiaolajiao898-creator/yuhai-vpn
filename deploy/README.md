# 部署说明（骨架阶段）

- `docker-compose.yml`（仓库根）：本地 PostgreSQL 17 + Redis。
- 官网 CDN 需配置重写规则：`/i/*` → `/i/index.html`（邀请落地页）。
- 生产网络分区（方案 2.4 六区隔离）与 mTLS 私有 CA 的部署脚本在阶段二补入。
- 发布流程：APK 构建 → `apksigner verify` → 生成 SHA-256 与证书指纹 → 后台创建 app_version（DRAFT）→ L3 审批 publish → 官网下载页自动读取。

## 邀请绑定与奖励开关

邀请绑定已接入 App API，仍要求访问令牌、设备签名、nonce 和每用户限流。绑定资格及重复请求由服务端确认，读取页面不会建立关系或发放奖励。

`INVITE_REWARDS_ENABLED=false` 是默认配置。App API 与 worker 应使用相同值；配置只接受 `true` 或 `false`，未设置为关闭，拼写错误拒绝启动。总开关关闭时 worker 不注册邀请订单消费者或奖励释放任务，因此既有待处理事件仍留在队列，也不会发放冻结中的奖励。

奖励规则另有独立的 `DISABLED/ENABLED` 状态，迁移默认 `DISABLED`。本轮未改变任何实际规则。总开关打开也不等于存在有效活动；页面仅在开关打开且存在当前有效规则时显示活动已开放。历史奖励仍可查询。

本次授权范围为完成接线且奖励保持关闭，未启动或部署 worker。后续启用前需另行确认运营规则、金额/比例、冻结期、限额、套餐和审核处理方式。不能把示例规则当作真实活动上线，也不能把本项目 worker 与其他下发软件混用。

隔离集成测试使用 `TEST_DATABASE_URL`、`TEST_MIGRATE_DATABASE_URL` 和 `TEST_REDIS_URL`；测试角色与业务运行连接分离。不要把正式库或共享 Redis 的配置传给测试。新增 API 测试只清理自己随机命名的数据，不执行 Redis 清空操作。
