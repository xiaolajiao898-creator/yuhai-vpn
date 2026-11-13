import 'package:flutter/material.dart';

import '../../app/theme/tokens.dart';

/// 「发现新版本」提示。强制升级时没有「稍后」。
class UpdatePrompt extends StatelessWidget {
  const UpdatePrompt({
    super.key,
    required this.version,
    required this.notes,
    required this.forced,
    required this.onUpgrade,
    this.onLater,
    this.progress,
  });

  final String version;
  final String notes;
  final bool forced;
  final VoidCallback onUpgrade;
  final VoidCallback? onLater;

  /// 0–1 时显示下载进度，代替两个按钮。
  final double? progress;

  @override
  Widget build(BuildContext context) {
    return Material(
      color: YhColors.paper,
      elevation: 8,
      borderRadius: BorderRadius.circular(YhRadius.surface),
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 360),
        child: Padding(
          padding: const EdgeInsets.fromLTRB(22, 22, 22, 16),
          child: Column(mainAxisSize: MainAxisSize.min, crossAxisAlignment: CrossAxisAlignment.stretch, children: [
            Text('发现新版本 $version', style: YhType.display(26, height: 1.1)),
            const SizedBox(height: 10),
            Text(notes.isEmpty ? '有一个新的安装包可以升级。' : notes, style: const TextStyle(height: 1.45, color: YhColors.sub)),
            if (forced) ...[
              const SizedBox(height: 8),
              const Text('这个版本已经不能继续使用，需要先升级。', style: TextStyle(color: YhColors.danger, fontWeight: FontWeight.w700)),
            ],
            const SizedBox(height: 16),
            if (progress != null)
              Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
                LinearProgressIndicator(value: progress == 0 ? null : progress, color: YhColors.accentInk, backgroundColor: YhColors.soft),
                const SizedBox(height: 8),
                Text(progress == 0 ? '正在准备下载' : '正在下载 ${(progress! * 100).round()}%', textAlign: TextAlign.center),
              ])
            else ...[
              FilledButton(onPressed: onUpgrade, child: const Text('一键升级')),
              if (!forced && onLater != null)
                TextButton(onPressed: onLater, child: const Text('稍后')),
            ],
          ]),
        ),
      ),
    );
  }
}
