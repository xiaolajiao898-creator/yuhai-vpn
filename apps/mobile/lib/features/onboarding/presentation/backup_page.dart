import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:qr_flutter/qr_flutter.dart';

import '../../../app/router.dart';
import '../../../app/theme/tokens.dart';
import 'onboarding_controller.dart';

class BackupArgs {
  const BackupArgs({required this.accountNo, required this.accountSecret});
  final String accountNo;
  final String accountSecret;
}

/// P03 账户密钥备份：密钥、二维码、复制、确认勾选。密钥丢失不可找回，这是唯一一次展示（方案 4.2）。
class BackupPage extends ConsumerStatefulWidget {
  const BackupPage({super.key, required this.args});
  final BackupArgs args;

  @override
  ConsumerState<BackupPage> createState() => _BackupPageState();
}

class _BackupPageState extends ConsumerState<BackupPage> {
  bool _confirmed = false;
  bool _busy = false;

  String get _grouped => widget.args.accountNo.replaceAllMapped(RegExp(r'(\d{4})(?=\d)'), (m) => '${m[1]} ');

  Future<void> _done() async {
    setState(() => _busy = true);
    try {
      await ref.read(authApiProvider).ackBackup();
    } catch (_) {
      // 标记失败不阻塞进入首页；首页会依据 backup_acknowledged 再次提醒
    }
    if (mounted) context.go(Routes.home);
  }

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    final secret = widget.args.accountSecret;
    return Scaffold(
      appBar: AppBar(title: const Text('备份账户密钥'), backgroundColor: Colors.transparent, automaticallyImplyLeading: false),
      body: SafeArea(
        child: ListView(
          padding: const EdgeInsets.symmetric(horizontal: YhSpace.page),
          children: [
            Text('这是恢复账户的唯一凭证', style: text.headlineMedium),
            const SizedBox(height: YhSpace.unit),
            Text('我们不保存你的手机号或邮箱，因此无法帮你找回密钥。请立即保存到密码管理器或截图保管。',
                style: text.bodyMedium?.copyWith(color: YhColors.haze)),
            const SizedBox(height: YhSpace.page),
            Container(
              padding: const EdgeInsets.all(YhSpace.panel),
              decoration: BoxDecoration(
                color: YhColors.current,
                border: Border.all(color: YhColors.reef),
                borderRadius: BorderRadius.circular(YhRadius.button),
              ),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text('账户号', style: text.labelSmall),
                  Text(_grouped, style: text.titleLarge?.copyWith(fontFamily: YhFont.numeric, fontFeatures: const [FontFeature.tabularFigures()])),
                  const SizedBox(height: YhSpace.unit * 2),
                  Text('账户密钥', style: text.labelSmall),
                  SelectableText(secret,
                      style: text.titleLarge?.copyWith(fontFamily: YhFont.numeric, color: YhColors.starlight, letterSpacing: 1.2)),
                  const SizedBox(height: YhSpace.unit * 2),
                  Center(
                    child: Container(
                      padding: const EdgeInsets.all(12),
                      decoration: BoxDecoration(color: YhColors.moonlight, borderRadius: BorderRadius.circular(YhRadius.listSelected)),
                      child: QrImageView(data: 'YHSECRET:$secret', size: 160, backgroundColor: YhColors.moonlight),
                    ),
                  ),
                  const SizedBox(height: YhSpace.unit * 2),
                  Row(children: [
                    Expanded(
                      child: OutlinedButton.icon(
                        onPressed: () async {
                          await Clipboard.setData(ClipboardData(text: secret));
                          HapticFeedback.lightImpact();
                          if (context.mounted) {
                            ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('已复制，请尽快粘贴到安全的地方')));
                          }
                        },
                        icon: const Icon(Icons.copy, size: 18),
                        label: const Text('复制密钥'),
                      ),
                    ),
                  ]),
                ],
              ),
            ),
            const SizedBox(height: YhSpace.page),
            CheckboxListTile(
              value: _confirmed,
              onChanged: (v) => setState(() => _confirmed = v ?? false),
              contentPadding: EdgeInsets.zero,
              controlAffinity: ListTileControlAffinity.leading,
              activeColor: YhColors.glow,
              title: const Text('我已妥善保存账户密钥，并知道丢失后无法找回'),
            ),
            const SizedBox(height: YhSpace.unit),
            FilledButton(onPressed: _confirmed && !_busy ? _done : null, child: const Text('进入宇海VPN')),
            const SizedBox(height: YhSpace.page),
          ],
        ),
      ),
    );
  }
}
