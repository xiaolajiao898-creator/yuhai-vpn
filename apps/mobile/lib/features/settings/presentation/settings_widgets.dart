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
            side: const BorderSide(color: YhColors.hair),
            borderRadius: BorderRadius.circular(YhRadius.surface),
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
        contentPadding: const EdgeInsets.fromLTRB(16, 6, 14, 6),
        minLeadingWidth: 40,
        leading: Container(
          width: 40,
          height: 40,
          decoration: BoxDecoration(
            color: YhColors.soft,
            borderRadius: BorderRadius.circular(YhRadius.listSelected),
          ),
          child: Icon(icon, size: 20, color: YhColors.accentInk),
        ),
        title: Text(title, style: const TextStyle(fontWeight: FontWeight.w600)),
        subtitle: subtitle == null
            ? null
            : Text(subtitle!, style: Theme.of(context).textTheme.labelSmall),
        trailing: onTap == null
            ? null
            : const Icon(Icons.arrow_forward_ios_rounded, size: 14, color: YhColors.faint),
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
          const Icon(Icons.cloud_off_outlined, size: 40, color: YhColors.accentInk),
          const SizedBox(height: 16),
          const Text('暂时无法加载，请检查网络后重试'),
          const SizedBox(height: 16),
          OutlinedButton(onPressed: onRetry, child: const Text('重新加载')),
        ]),
      ));
}
