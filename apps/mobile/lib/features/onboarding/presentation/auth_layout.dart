import 'package:flutter/material.dart';

import '../../../app/theme/tokens.dart';

/// 登录注册类页面的统一外框：手机上铺满，电脑大窗口里居中成一张卡片（最宽 440）。
class AuthLayout extends StatelessWidget {
  const AuthLayout({super.key, required this.child, this.title});
  final Widget child;
  final String? title;

  @override
  Widget build(BuildContext context) {
    final wide = MediaQuery.sizeOf(context).width >= 720;
    final body = SingleChildScrollView(
      padding: const EdgeInsets.symmetric(
          horizontal: YhSpace.page, vertical: YhSpace.page),
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 440),
        child: child,
      ),
    );
    return SafeArea(
      child: Center(
        child: wide
            ? Container(
                margin: const EdgeInsets.all(YhSpace.page),
                decoration: BoxDecoration(
                  color: YhColors.current,
                  borderRadius: BorderRadius.circular(YhRadius.sheet),
                  border: Border.all(color: YhColors.reef),
                ),
                child: body,
              )
            : body,
      ),
    );
  }
}

/// 设备数量已满时，让用户选一台移除。
Future<String?> pickDeviceToRemove(
    BuildContext context, List<Map<String, dynamic>> devices) {
  return showModalBottomSheet<String>(
    context: context,
    builder: (ctx) => SafeArea(
      child: Padding(
        padding: const EdgeInsets.all(YhSpace.panel),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text('设备数量已达上限', style: Theme.of(ctx).textTheme.titleLarge),
            const SizedBox(height: YhSpace.unit),
            Text('选择一台设备移除后，本设备才能登录。',
                style: Theme.of(ctx).textTheme.labelSmall),
            const SizedBox(height: YhSpace.unit * 2),
            for (final d in devices)
              ListTile(
                contentPadding: EdgeInsets.zero,
                title: Text((d['display_name'] ?? d['platform'] ?? '').toString()),
                subtitle: Text(d['last_seen_at'] != null
                    ? '最近活跃 ${d['last_seen_at']}'
                    : '从未活跃'),
                trailing: const Icon(Icons.remove_circle_outline,
                    color: YhColors.coral),
                onTap: () => Navigator.pop(ctx, d['id'] as String),
              ),
          ],
        ),
      ),
    ),
  );
}
