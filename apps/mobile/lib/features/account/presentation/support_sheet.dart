import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../../../app/theme/tokens.dart';
import '../../../core/support_config.dart';

Future<void> showSupportSheet(BuildContext context) {
  return showModalBottomSheet<void>(
    context: context,
    showDragHandle: true,
    builder: (context) {
      final text = Theme.of(context).textTheme;
      return SafeArea(
        child: Padding(
          padding: const EdgeInsets.fromLTRB(24, 0, 24, 24),
          child: Column(mainAxisSize: MainAxisSize.min, crossAxisAlignment: CrossAxisAlignment.stretch, children: [
            Text(kSupportLabel, style: text.headlineMedium),
            const SizedBox(height: 8),
            Text(
              kSupportUrlIsPlaceholder
                  ? '客服链接还没换成正式的 Telegram。可以先复制保存。不要把密码或账户密钥发给任何人。'
                  : '在 Telegram 里联系我们。请不要把密码或账户密钥发给任何人。',
              style: text.bodyMedium?.copyWith(color: YhColors.sub),
            ),
            const SizedBox(height: 16),
            SelectableText(kSupportUrl, style: text.bodyLarge),
            const SizedBox(height: 16),
            FilledButton(
              onPressed: () async {
                await Clipboard.setData(const ClipboardData(text: kSupportUrl));
                if (context.mounted) {
                  Navigator.pop(context);
                  ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('已复制客服链接')));
                }
              },
              child: const Text('复制链接'),
            ),
          ]),
        ),
      );
    },
  );
}
