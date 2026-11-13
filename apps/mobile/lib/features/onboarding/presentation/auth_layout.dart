import 'package:flutter/material.dart';

import '../../../app/theme/tokens.dart';
import '../../../core/widgets/brand.dart';

/// 登录注册类页面的统一外框：手机上铺满，电脑大窗口里居中成一张卡片（最宽 440）。
///
/// 给了 [footer] 时，它固定在底部、不随内容滚动：内容比窗口高时只滚动上面的内容，
/// 主按钮一直看得见（例如注册页在电脑默认窗口里）。内容不高时，内容和 [footer] 一起居中。
class AuthLayout extends StatelessWidget {
  const AuthLayout({super.key, required this.child, this.title, this.footer});
  final Widget child;
  final String? title;

  /// 固定在底部的操作区：主按钮、次要链接、错误提示。
  final Widget? footer;

  @override
  Widget build(BuildContext context) {
    final wide = MediaQuery.sizeOf(context).width >= 720;
    final footer = this.footer;
    final Widget scroll = SingleChildScrollView(
      padding: EdgeInsets.fromLTRB(YhSpace.page, YhSpace.page, YhSpace.page,
          footer == null ? YhSpace.page : YhSpace.unit),
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 440),
        child: child,
      ),
    );
    // 有固定底栏时，滚动区下沿淡出，不再是一刀切开的输入框。
    final Widget content = footer == null
        ? scroll
        : ShaderMask(
            blendMode: BlendMode.dstIn,
            shaderCallback: (rect) => LinearGradient(
              begin: Alignment.topCenter,
              end: Alignment.bottomCenter,
              colors: const [Colors.white, Colors.white, Colors.transparent],
              stops: [0, rect.height <= 0 ? 1 : (1 - 28 / rect.height).clamp(0.0, 1.0), 1],
            ).createShader(rect),
            child: scroll,
          );
    final Widget body = footer == null
        ? content
        : Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              Flexible(child: content),
              Padding(
                padding: const EdgeInsets.fromLTRB(
                    YhSpace.page, YhSpace.unit, YhSpace.page, YhSpace.page),
                child: ConstrainedBox(
                  constraints: const BoxConstraints(maxWidth: 440),
                  child: footer,
                ),
              ),
            ],
          );
    return TideBackdrop(
      linesTop: 0.84,
      glow: 0.8,
      child: SafeArea(
        child: Center(
          child: wide
              ? Container(
                  margin: const EdgeInsets.all(YhSpace.page),
                  decoration: BoxDecoration(
                    color: YhColors.current,
                    borderRadius: BorderRadius.circular(YhRadius.sheet),
                    border: Border.all(color: YhColors.hairStrong),
                    boxShadow: const [BoxShadow(color: Color(0x2614201C), blurRadius: 50, offset: Offset(0, 24))],
                  ),
                  child: body,
                )
              : body,
        ),
      ),
    );
  }
}

String _deviceTitle(Map<String, dynamic> device) {
  final name = (device['display_name'] ?? '').toString().trim();
  final platform = (device['platform'] ?? '').toString().trim();
  final model = (device['model'] ?? '').toString().trim();
  if (name.isNotEmpty && name.toLowerCase() != platform.toLowerCase()) {
    return name;
  }
  if (model.isNotEmpty && model.toLowerCase() != platform.toLowerCase()) {
    return model;
  }
  if (platform.isNotEmpty) return platform;
  return '设备 ${device['id'] ?? ''}';
}

/// 设备数量已满时，让用户选一台移除。关闭弹层表示这次不登录。
Future<String?> pickDeviceToRemove(
    BuildContext context, List<Map<String, dynamic>> devices,
    {int limit = 0}) {
  final cap = limit > 0 ? '（最多 $limit 台）' : '';
  return showModalBottomSheet<String>(
    context: context,
    builder: (ctx) => SafeArea(
      child: Padding(
        padding: const EdgeInsets.all(YhSpace.panel),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text('设备数量已达上限$cap', style: Theme.of(ctx).textTheme.titleLarge),
            const SizedBox(height: YhSpace.unit),
            Text('当前套餐的设备名额已满。选择一台移除后，这台手机才能登录。',
                style: Theme.of(ctx).textTheme.labelSmall),
            const SizedBox(height: YhSpace.unit * 2),
            for (final d in devices)
              ListTile(
                contentPadding: EdgeInsets.zero,
                title: Text(_deviceTitle(d)),
                subtitle: Text(d['last_seen_at'] != null
                    ? '最近活跃 ${d['last_seen_at']}'
                    : '从未活跃'),
                trailing: const Icon(Icons.remove_circle_outline,
                    color: YhColors.coral),
                onTap: () => Navigator.pop(ctx, d['id']?.toString()),
              ),
          ],
        ),
      ),
    ),
  );
}
