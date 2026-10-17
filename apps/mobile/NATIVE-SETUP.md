# 原生工程接续说明

Android / iOS 工程已从当前本地 Flutter 官方模板补齐；保留原有设备密钥通道。

- Android 使用 `FlutterFragmentActivity`、`USE_BIOMETRIC` 和 AppCompat 主题，支持系统生物识别或锁屏密码验证。关闭系统自动备份，避免迁移失效的安全存储内容。
- iOS 已加入 Face ID 用途说明；按当前 Flutter UIScene 模板在引擎初始化后注册插件与设备密钥通道，Xcode Sources 已包含 `DeviceKeyChannel.swift`。
- 设备移除页面只有系统身份验证成功后才发送带设备签名的请求；模拟网页通过显式演示依赖替换系统验证。
- Android 正式构建不使用模板调试签名，发布前需在安全构建环境配置正式签名。iOS 仍需开发者签名与真机调试。

本轮仅检查配置文件与 Flutter 页面，不代表 Android / iOS 原生编译或真机生物验证已通过。真实 VPN 引擎仍未接入。原生构建需要安装对应 Android SDK / JDK、Xcode 及插件依赖；发布时配置 HTTPS 的 `YH_API_BASE`。
