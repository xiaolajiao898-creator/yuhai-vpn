import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../../app/theme/tokens.dart';
import '../data/connection_preferences.dart';
import '../data/engine_capabilities.dart';
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

  String _protocolLine(ProtocolPreference protocol) => switch (protocol) {
        ProtocolPreference.auto => '优先使用当前设备能连上的线路',
        ProtocolPreference.vlessReality => 'Reality：把流量伪装成访问普通网站，适合限制较严的网络',
        ProtocolPreference.wireguard => 'WireGuard：当前自建节点使用的加密隧道',
        ProtocolPreference.trojan => 'Trojan：伪装成普通网页访问',
        ProtocolPreference.shadowsocks2022 => 'Shadowsocks 2022：轻量代理',
      };

  Future<void> _killSwitchHelp(BuildContext context) {
    return showModalBottomSheet<void>(
      context: context,
      showDragHandle: true,
      builder: (context) => SafeArea(
        child: Padding(
          padding: const EdgeInsets.fromLTRB(24, 0, 24, 24),
          child: Column(mainAxisSize: MainAxisSize.min, crossAxisAlignment: CrossAxisAlignment.start, children: [
            Text('断网保护', style: Theme.of(context).textTheme.headlineMedium),
            const SizedBox(height: 8),
            const Text('这个版本还不能自己阻断「VPN 断开后的其他流量」。在安卓上可以到系统设置里打开：'),
            const SizedBox(height: 8),
            const Text('设置 → 网络和互联网 → VPN → 宇海VPN → 始终开启 VPN，并打开「阻断没有 VPN 的连接」。'),
            const SizedBox(height: 8),
            const Text('Windows 客户端目前没有对应的系统开关。代码签名和这项保护都还需要后续引擎支持。'),
          ]),
        ),
      ),
    );
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
                    padding: EdgeInsets.fromLTRB(16, 8, 16, 8),
                    child: Text(
                        '偏好保存在本机。当前自建节点按 WireGuard 连接；出口线路的协议由线路本身决定。')),
                for (final protocol in ProtocolPreference.values)
                  ListTile(
                    key: ValueKey('protocol-${protocol.wireValue}'),
                    title: Text(protocol.label),
                    subtitle: Text(_protocolLine(protocol)),
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
            const SizedBox(height: 8),
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
            const SizedBox(height: 16),
            const Text(
                'Hysteria2：基于 QUIC，适合丢包较高的网络。由线路决定，不能在这里单独指定。'),
            const SizedBox(height: 16),
            SettingsPanel(
                child: Column(children: [
              SwitchListTile(
                key: const ValueKey('automatic-connect'),
                contentPadding: const EdgeInsets.symmetric(
                    horizontal: YhSpace.panel, vertical: 4),
                title: const Text('自动连接'),
                subtitle: const Text('打开 App 时，如果已有可用套餐，自动开始连接'),
                value: data.autoConnect,
                onChanged: _saving
                    ? null
                    : (value) {
                        final controller =
                            ref.read(connectionPreferencesProvider.notifier);
                        _save(() => controller.setAutoConnect(value),
                            message: '自动连接已保存');
                      },
              ),
              const Divider(height: 1),
              ListTile(
                key: const ValueKey('kill-switch-guidance'),
                title: const Text('断网保护'),
                subtitle: const Text(kKillSwitchEnforced
                    ? '隧道意外断开时阻断其他流量'
                    : '当前引擎还不能强制阻断。可以查看系统里的设置方法'),
                trailing: const Text('说明'),
                onTap: () => _killSwitchHelp(context),
              ),
              const Divider(height: 1),
              SwitchListTile(
                key: const ValueKey('split-tunnel-unavailable'),
                title: const Text('按应用分流'),
                subtitle: const Text('引擎尚未接入。打开也不会改变流量走向'),
                value: false,
                onChanged: kPerAppSplitTunnelAvailable ? (_) {} : null,
              ),
              const Divider(height: 1),
              SwitchListTile(
                key: const ValueKey('bypass-mainland-unavailable'),
                title: const Text('绕过中国大陆'),
                subtitle: const Text('引擎尚未接入。打开也不会让大陆流量直连'),
                value: false,
                onChanged: kBypassMainlandAvailable ? (_) {} : null,
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
