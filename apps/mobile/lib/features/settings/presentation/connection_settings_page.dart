import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../../app/theme/tokens.dart';
import '../data/connection_preferences.dart';
import 'settings_widgets.dart';

class ConnectionSettingsPage extends ConsumerStatefulWidget {
  const ConnectionSettingsPage({super.key});

  @override
  ConsumerState<ConnectionSettingsPage> createState() =>
      _ConnectionSettingsPageState();
}

class _ConnectionSettingsPageState
    extends ConsumerState<ConnectionSettingsPage> {
  bool _saving = false;
  String? _error;

  Future<void> _save(Future<void> Function() action,
      {required String message}) async {
    if (_saving) {
      return;
    }
    setState(() {
      _saving = true;
      _error = null;
    });
    try {
      await action();
      if (!mounted) {
        return;
      }
      ScaffoldMessenger.of(context)
          .showSnackBar(SnackBar(content: Text(message)));
    } catch (_) {
      if (mounted) {
        setState(() => _error = '设置未保存，请重试。仍使用上次保存的设置。');
      }
    } finally {
      if (mounted) {
        setState(() => _saving = false);
      }
    }
  }

  Future<void> _chooseProtocol(ProtocolPreference current) async {
    final selected = await showModalBottomSheet<ProtocolPreference>(
      context: context,
      showDragHandle: true,
      builder: (context) => SafeArea(
          child: SingleChildScrollView(
        child: Padding(
            padding: const EdgeInsets.only(bottom: 16),
            child: Column(
              mainAxisSize: MainAxisSize.min,
              children: [
                Text('选择协议偏好', style: Theme.of(context).textTheme.titleLarge),
                const Padding(
                    padding: EdgeInsets.all(16),
                    child: Text('偏好保存在本机，真实连接引擎接入后生效。')),
                for (final protocol in ProtocolPreference.values)
                  ListTile(
                    key: ValueKey('protocol-${protocol.wireValue}'),
                    title: Text(protocol.label),
                    selected: protocol == current,
                    trailing: protocol == current
                        ? const Icon(Icons.check, color: YhColors.starlight)
                        : null,
                    onTap: () => Navigator.pop(context, protocol),
                  ),
              ],
            )),
      )),
    );
    if (selected == null || selected == current || !mounted) {
      return;
    }
    final controller = ref.read(connectionPreferencesProvider.notifier);
    await _save(() => controller.setProtocol(selected),
        message: '协议偏好已保存，真实引擎接入后生效');
  }

  @override
  Widget build(BuildContext context) {
    final preferences = ref.watch(connectionPreferencesProvider);
    return Scaffold(
      appBar: AppBar(title: const Text('连接设置')),
      body: preferences.when(
        loading: () => const Center(child: CircularProgressIndicator()),
        error: (_, __) => Center(
            child: Padding(
          padding: const EdgeInsets.all(YhSpace.page),
          child: Column(mainAxisSize: MainAxisSize.min, children: [
            const Icon(Icons.settings_backup_restore,
                size: 40, color: YhColors.haze),
            const SizedBox(height: 16),
            const Text('暂时无法读取本机设置，请重试'),
            const SizedBox(height: 16),
            OutlinedButton(
                onPressed: () => ref.invalidate(connectionPreferencesProvider),
                child: const Text('重新读取')),
          ]),
        )),
        data: (data) => ListView(
          padding: const EdgeInsets.all(YhSpace.page),
          children: [
            const SettingsPanel(
                child: Padding(
              padding: EdgeInsets.all(YhSpace.panel),
              child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Icon(Icons.science_outlined, color: YhColors.starlight),
                    SizedBox(height: 12),
                    Text('当前为连接演示',
                        style: TextStyle(
                            fontSize: 18, fontWeight: FontWeight.w700)),
                    SizedBox(height: 8),
                    Text('模拟连接不会建立真实 VPN 隧道。协议偏好可先保存，接入真实引擎后生效。'),
                  ]),
            )),
            const SizedBox(height: 24),
            SettingsPanel(
                child: Column(children: [
              ListTile(
                key: const ValueKey('select-connection-protocol'),
                contentPadding: const EdgeInsets.symmetric(
                    horizontal: YhSpace.panel, vertical: 4),
                leading: const Icon(Icons.alt_route, color: YhColors.haze),
                title: const Text('协议偏好'),
                subtitle: Text(data.protocol.label),
                trailing: const Icon(Icons.chevron_right, color: YhColors.haze),
                enabled: !_saving,
                onTap: _saving ? null : () => _chooseProtocol(data.protocol),
              ),
              const Divider(height: 1),
              SwitchListTile(
                key: const ValueKey('reduce-connection-motion'),
                contentPadding: const EdgeInsets.symmetric(
                    horizontal: YhSpace.panel, vertical: 4),
                title: const Text('减少连接动画'),
                subtitle: const Text('立即减少首页连接动效'),
                value: data.reduceMotion,
                onChanged: _saving
                    ? null
                    : (value) {
                        final controller =
                            ref.read(connectionPreferencesProvider.notifier);
                        _save(() => controller.setReduceMotion(value),
                            message: '动画设置已保存');
                      },
              ),
            ])),
            const SizedBox(height: 24),
            const SettingsPanel(
                child: Column(children: [
              ListTile(
                key: ValueKey('automatic-connect-unavailable'),
                enabled: false,
                title: Text('自动连接'),
                subtitle: Text('开发中：当前版本需要手动连接'),
                trailing: Text('开发中'),
              ),
              Divider(height: 1),
              ListTile(
                key: ValueKey('kill-switch-unavailable'),
                enabled: false,
                title: Text('断网保护'),
                subtitle: Text('开发中：当前版本在连接意外中断时不会阻断其他流量'),
                trailing: Text('开发中'),
              ),
            ])),
            if (_saving)
              const Padding(
                padding: EdgeInsets.only(top: 20),
                child: Center(child: Text('正在保存…')),
              ),
            if (_error != null)
              Padding(
                padding: const EdgeInsets.only(top: 20),
                child: Text(_error!,
                    style: const TextStyle(color: YhColors.coral)),
              ),
            const SizedBox(height: 24),
            Text('这些偏好只保存在本机。',
                style: Theme.of(context).textTheme.labelSmall,
                textAlign: TextAlign.center),
          ],
        ),
      ),
    );
  }
}
