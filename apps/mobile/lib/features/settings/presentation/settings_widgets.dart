import 'package:flutter/material.dart';
import '../../../app/theme/tokens.dart';

class SettingsPanel extends StatelessWidget {
  const SettingsPanel({super.key, required this.child});
  final Widget child;

  @override
  Widget build(BuildContext context) => SizedBox(
        width: double.infinity,
        child: Material(
          color: YhColors.current,
          shape: RoundedRectangleBorder(
            side: const BorderSide(color: YhColors.reef),
            borderRadius: BorderRadius.circular(YhRadius.button),
          ),
          clipBehavior: Clip.antiAlias,
          child: child,
        ),
      );
}

class SettingsRow extends StatelessWidget {
  const SettingsRow(
      {super.key,
      required this.icon,
      required this.title,
      this.subtitle,
      this.onTap});
  final IconData icon;
  final String title;
  final String? subtitle;
  final VoidCallback? onTap;

  @override
  Widget build(BuildContext context) => ListTile(
        contentPadding:
            const EdgeInsets.symmetric(horizontal: YhSpace.panel, vertical: 4),
        leading: Icon(icon, color: YhColors.haze),
        title: Text(title),
        subtitle: subtitle == null
            ? null
            : Text(subtitle!, style: Theme.of(context).textTheme.labelSmall),
        trailing: onTap == null
            ? null
            : const Icon(Icons.chevron_right, color: YhColors.haze),
        onTap: onTap,
      );
}

class AccountLoadError extends StatelessWidget {
  const AccountLoadError({super.key, required this.onRetry});
  final VoidCallback onRetry;

  @override
  Widget build(BuildContext context) => Center(
          child: Padding(
        padding: const EdgeInsets.all(YhSpace.page),
        child: Column(mainAxisSize: MainAxisSize.min, children: [
          const Icon(Icons.cloud_off_outlined, size: 40, color: YhColors.haze),
          const SizedBox(height: 16),
          const Text('暂时无法加载，请检查网络后重试'),
          const SizedBox(height: 16),
          OutlinedButton(onPressed: onRetry, child: const Text('重新加载')),
        ]),
      ));
}
