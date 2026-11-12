/// 客服入口：Telegram 链接在构建时通过 --dart-define=YH_SUPPORT_TG=https://t.me/<用户名> 注入。
/// 未注入（或格式不对）时，App 里所有「联系客服」入口都隐藏，不再展示占位账号，
/// 以免用户去联系一个并不属于我们的 Telegram 用户名。
const kSupportUrl = String.fromEnvironment('YH_SUPPORT_TG');
const kSupportLabel = 'Telegram 客服';

/// 只接受 https://t.me/<用户名>（5–32 位字母、数字、下划线），不接受其它域名或带参数的链接。
final bool kSupportAvailable = RegExp(r'^https://t\.me/[A-Za-z][A-Za-z0-9_]{4,31}$').hasMatch(kSupportUrl);
