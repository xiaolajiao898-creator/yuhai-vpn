import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../../app/router.dart';
import '../../../app/theme/tokens.dart';
import '../../../core/network/api_client.dart';
import '../data/auth_api.dart';
import 'onboarding_controller.dart';

/// P04 恢复账户：输入密钥（扫码在第 7–8 周随 mobile_scanner 接入）。设备已满时选择移除一台。
class RestorePage extends ConsumerStatefulWidget {
  const RestorePage({super.key});

  @override
  ConsumerState<RestorePage> createState() => _RestorePageState();
}

class _RestorePageState extends ConsumerState<RestorePage> {
  final _ctl = TextEditingController();
  bool _busy = false;
  String? _error;

  Future<void> _restore() async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      await ref.read(sessionProvider.notifier).restore(_ctl.text);
      if (mounted) context.go(Routes.home);
    } on DeviceLimitReached catch (e) {
      if (!mounted) return;
      final removeId = await _pickDevice(e.devices);
      if (removeId == null) return;
      try {
        await ref.read(sessionProvider.notifier).replaceDevice(e.ticket, removeId);
        if (mounted) context.go(Routes.home);
      } on ApiException catch (e2) {
        setState(() => _error = e2.message);
      }
    } on ApiException catch (e) {
      setState(() => _error = e.code == 42902 ? '尝试次数过多，请稍后再试' : e.message);
    } catch (_) {
      setState(() => _error = '网络不可用，请稍后再试');
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<String?> _pickDevice(List<Map<String, dynamic>> devices) {
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
              Text('选择一台设备移除后，本设备才能登录。', style: Theme.of(ctx).textTheme.labelSmall),
              const SizedBox(height: YhSpace.unit * 2),
              for (final d in devices)
                ListTile(
                  contentPadding: EdgeInsets.zero,
                  title: Text(d['display_name'] ?? d['platform'] ?? ''),
                  subtitle: Text(d['last_seen_at'] != null ? '最近活跃 ${d['last_seen_at']}' : '从未活跃'),
                  trailing: const Icon(Icons.remove_circle_outline, color: YhColors.coral),
                  onTap: () => Navigator.pop(ctx, d['id'] as String),
                ),
            ],
          ),
        ),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return Scaffold(
      appBar: AppBar(title: const Text('恢复账户'), backgroundColor: Colors.transparent),
      body: SafeArea(
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: YhSpace.page),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              const SizedBox(height: YhSpace.unit * 2),
              Text('输入账户密钥', style: text.headlineMedium),
              const SizedBox(height: YhSpace.unit),
              Text('形如 YH-XXXX-XXXX-XXXX-XXXX-XXXX，大小写与连字符可省略。', style: text.labelSmall),
              const SizedBox(height: YhSpace.page),
              TextField(
                controller: _ctl,
                autocorrect: false,
                enableSuggestions: false,
                textCapitalization: TextCapitalization.characters,
                style: const TextStyle(fontFamily: YhFont.numeric, letterSpacing: 1.2),
                decoration: InputDecoration(hintText: 'YH-…', errorText: _error),
                onSubmitted: (_) => _restore(),
              ),
              const SizedBox(height: YhSpace.page),
              FilledButton(onPressed: _busy ? null : _restore, child: const Text('恢复')),
            ],
          ),
        ),
      ),
    );
  }

  @override
  void dispose() {
    _ctl.dispose();
    super.dispose();
  }
}
