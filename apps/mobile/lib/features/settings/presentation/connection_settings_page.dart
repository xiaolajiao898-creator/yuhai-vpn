import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
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
        ProtocolPreference.wireguard => 'WireGuard：自建节点用的加密隧道',
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
          child: Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text('断网保护', style: Theme.of(context).textTheme.headlineMedium),
                const SizedBox(height: 8),
                const Text(kKillSwitchEnforced
                    ? '系统的「始终开启 VPN」已由应用打开。'
                    : '应用自己不会改系统的「始终开启 VPN」。重连时会尽量把流量留在 VPN 接口里；进程被系统杀掉之后，只有下面这两项能继续拦住。'),
                const SizedBox(height: 8),
                const Text(
                    '安卓：设置 → 网络和互联网 → VPN → 宇海VPN → 打开「始终开启 VPN」和「无 VPN 时阻止连接」。'),
                const SizedBox(height: 8),
                const Text('IPv6 和 DNS 会进隧道；隧道还在、内核已停时，这些包会被丢掉，不会改走本地网络。'),
                const SizedBox(height: 8),
                const Text(
                    'Windows：用防火墙拦住除本程序和 xray 以外的 IPv4/IPv6 出站。规则随进程退出消失。DHCP 仍可能走本地。请用管理员身份运行。'),
                const SizedBox(height: 16),
                Align(
                  alignment: Alignment.centerLeft,
                  child: OutlinedButton(
                    onPressed: () async {
                      try {
                        await const MethodChannel('yuhai/vpn')
                            .invokeMethod<bool>('openVpnSettings');
                      } catch (_) {}
                    },
                    child: const Text('打开系统 VPN 设置'),
                  ),
                ),
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
                    child: Text('保存在这台设备上。自建节点用 WireGuard，出口线路用它自己的协议。')),
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
        message: '已保存。真正连接时会按这个偏好。');
  }

  @override
  Widget build(BuildContext context) {
    final preferences = ref.watch(connectionPreferencesProvider);
    return Scaffold(
      appBar: AppBar(title: const Text('连接设置')),
      body: Column(children: [
        if (_saving)
          const Padding(
            padding: EdgeInsets.fromLTRB(24, 12, 24, 0),
            child: Center(child: Text('正在保存…')),
          ),
        if (_error != null)
          Padding(
            padding: const EdgeInsets.fromLTRB(24, 12, 24, 0),
            child: Text(_error!, style: const TextStyle(color: YhColors.coral)),
          ),
        Expanded(
            child: preferences.when(
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
                  onPressed: () =>
                      ref.invalidate(connectionPreferencesProvider),
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
                  trailing:
                      const Icon(Icons.chevron_right, color: YhColors.haze),
                  enabled: !_saving,
                  onTap: _saving ? null : () => _chooseProtocol(data.protocol),
                ),
                const Divider(height: 1),
                SwitchListTile(
                  key: const ValueKey('reduce-connection-motion'),
                  contentPadding: const EdgeInsets.symmetric(
                      horizontal: YhSpace.panel, vertical: 4),
                  title: const Text('减少连接动画'),
                  subtitle: const Text('少一点首页连接时的动画'),
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
              const Text('Hysteria2 适合网络容易丢包的时候，由线路决定，这里不能单独选。'),
              const SizedBox(height: 16),
              SettingsPanel(
                  child: Column(children: [
                SwitchListTile(
                  key: const ValueKey('automatic-connect'),
                  contentPadding: const EdgeInsets.symmetric(
                      horizontal: YhSpace.panel, vertical: 4),
                  title: const Text('自动连接'),
                  subtitle: const Text('下次打开 App，如果还能用，就自动连上'),
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
                SwitchListTile(
                  key: const ValueKey('auto-recover'),
                  contentPadding: const EdgeInsets.symmetric(
                      horizontal: YhSpace.panel, vertical: 4),
                  title: const Text('自动重连并切换线路'),
                  subtitle: const Text(
                      '掉线、卡死或切换 Wi-Fi / 移动数据时重连。当前线路不行，就改到同一国家或地区的其他线路'),
                  value: data.autoRecover,
                  onChanged: _saving
                      ? null
                      : (value) {
                          final controller =
                              ref.read(connectionPreferencesProvider.notifier);
                          _save(() => controller.setAutoRecover(value),
                              message: '自动重连已保存');
                        },
                ),
                const Divider(height: 1),
                SwitchListTile(
                  key: const ValueKey('leak-protect'),
                  contentPadding: const EdgeInsets.symmetric(
                      horizontal: YhSpace.panel, vertical: 4),
                  title: const Text('断网保护'),
                  subtitle: const Text('重连期间拦住本地网络。只有点「断开」才恢复。默认开启'),
                  value: data.leakProtect,
                  onChanged: _saving
                      ? null
                      : (value) {
                          final controller =
                              ref.read(connectionPreferencesProvider.notifier);
                          _save(() => controller.setLeakProtect(value),
                              message: '断网保护已保存');
                        },
                ),
                const Divider(height: 1),
                ListTile(
                  key: const ValueKey('kill-switch-guidance'),
                  title: const Text('系统里的断网保护'),
                  subtitle: const Text(kKillSwitchEnforced
                      ? '隧道意外断开时阻断其他流量'
                      : '应用拦不住进程被杀之后的流量。点「说明」看系统里怎么开'),
                  trailing: const Text('说明'),
                  onTap: () => _killSwitchHelp(context),
                ),
                const Divider(height: 1),
                SwitchListTile(
                  key: const ValueKey('split-tunnel-unavailable'),
                  title: const Text('按应用分流'),
                  subtitle: const Text('还没做好。\n打开也不会分流'),
                  value: false,
                  onChanged: kPerAppSplitTunnelAvailable ? (_) {} : null,
                ),
                const Divider(height: 1),
                SwitchListTile(
                  key: const ValueKey('bypass-mainland-unavailable'),
                  title: const Text('绕过中国大陆'),
                  subtitle: const Text('还没做好。\n打开也不会直连大陆'),
                  value: false,
                  onChanged: kBypassMainlandAvailable ? (_) {} : null,
                ),
              ])),
              const SizedBox(height: 24),
              Text('这些偏好只保存在本机。',
                  style: Theme.of(context).textTheme.labelSmall,
                  textAlign: TextAlign.center),
            ],
          ),
        )),
      ]),
    );
  }
}
