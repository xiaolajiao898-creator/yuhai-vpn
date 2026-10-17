# 宇海VPN（yhvpn.io）

跨平台加密 VPN 的完整源码：Go 后端、Flutter 客户端、React 管理后台和 Astro 官网放在同一个仓库里。

## 目录结构

```
yuhai-vpn/
├── api/openapi/        接口契约（OpenAPI 3.1）：app.yaml / admin.yaml / node.yaml
├── server/             Go 后端（模块化单体）
│   ├── cmd/api         App 接口服务
│   ├── cmd/admin-api   管理后台接口服务
│   ├── cmd/worker      后台任务（订单、到账、邀请奖励等）
│   ├── cmd/node-*      节点相关组件（node-api / node-agent / node-sync 等）
│   ├── internal/       业务模块（账户、认证、套餐、订单、出口线路 egress 等）
│   │   └── db/migrations  数据库迁移（goose）
│   └── scripts/        本地开发用的数据库角色与种子数据
├── apps/mobile/        Flutter 客户端（Android / iOS / macOS / Windows 工程）
├── apps/admin-web/     管理后台（React + TypeScript + Vite）
├── apps/website/       官网与下载页（Astro）
├── deploy/             部署与打包脚本（不含任何密钥，运行时从环境变量 / 本地文件读取）
└── docs/               设计文档与开发说明
```

## 本地开发

```bash
cp .env.example .env     # 示例值仅供本地开发，生产环境必须换成自己的密钥
make up                  # 启动本地 PostgreSQL + Redis（docker-compose）
make migrate             # 执行数据库迁移
make run-api             # App 接口
make run-admin           # 管理后台接口
make run-worker          # 后台任务
```

- 后台前端：`cd apps/admin-web && npm ci && npm run dev`
- 官网：`cd apps/website && npm ci && npm run dev`
- 客户端：`cd apps/mobile && flutter pub get && flutter run`

Android 多协议版需要 Xray 内核 `libv2ray.aar`（来自 AndroidLibXrayLite，体积较大未入库），
下载地址与校验值见 `deploy/build-android-candidate.sh`。

打包脚本（`deploy/build-*.sh`、`deploy/android-emulator.sh`）的工具链和钥匙位置都可以用环境变量指定：
`FLUTTER_HOME`（默认 `~/flutter`）、`JAVA_HOME`（JDK 17）、`ANDROID_SDK_ROOT`（默认 `~/Library/Android/sdk`）、
`PUB_CACHE`、`YUHAI_ANDROID_KEY_DIR`（签名钥匙目录，默认 `~/.yuhai-android`）、`YUHAI_FUND_KEY_FILE`（收款验签公钥文件，默认 `~/.yuhai-fund-key`）、`OUT_DIR`（产物输出目录，默认桌面）。

更详细的设计与开发记录见 `docs/系统设计方案_v1.0.md` 和 `docs/开发说明.md`。

## 安全说明

仓库里不含任何服务器地址、账号密码、签名钥匙或节点凭据。`.env`、签名文件、节点导入文件等都已在 `.gitignore` 中排除，请不要提交。
