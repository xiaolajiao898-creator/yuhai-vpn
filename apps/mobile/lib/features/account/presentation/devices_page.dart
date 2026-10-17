import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../../app/theme/tokens.dart';
import '../../home/data/me_api.dart';
import '../../settings/data/device_removal_auth.dart';
import '../../settings/presentation/settings_widgets.dart';
import '../data/account_api.dart';

class DevicesPage extends ConsumerStatefulWidget {
  const DevicesPage({super.key});
  @override
  ConsumerState<DevicesPage> createState() => _DevicesPageState();
}

class _DevicesPageState extends ConsumerState<DevicesPage> {
  String? _removing;
  String? _error;

  Future<void> _remove(AccountDevice device) async {
    if (device.isCurrent || _removing != null) return;
    final confirmed = await showDialog<bool>(
        context: context,
        builder: (context) => AlertDialog(
              title: const Text('移除这台设备？'),
              content: Text('「${device.displayName}」将退出当前账户。再次登录需要账户密钥。'),
              actions: [
                TextButton(
                    onPressed: () => Navigator.pop(context, false),
                    child: const Text('取消')),
                TextButton(
                    onPressed: () => Navigator.pop(context, true),
                    child: const Text('确认移除',
                        style: TextStyle(color: YhColors.coral))),
              ],
            ));
    if (confirmed != true || !mounted) return;
    setState(() {
      _removing = device.id;
      _error = null;
    });
    var identityVerified = false;
    try {
      final verified =
          await ref.read(deviceRemovalAuthProvider).confirmIdentity();
      if (!mounted) return;
      if (!verified) {
        setState(() => _error = '身份验证未完成，设备未移除。');
        return;
      }
      identityVerified = true;
      await ref.read(accountApiProvider).removeDevice(device.id);
      if (!mounted) return;
      ref.invalidate(meProvider);
      ref.invalidate(devicesProvider);
      await ref.read(devicesProvider.future);
      if (mounted) {
        ScaffoldMessenger.of(context)
            .showSnackBar(const SnackBar(content: Text('设备已移除')));
      }
    } catch (_) {
      if (mounted) {
        setState(() => _error = identityVerified
            ? '暂时无法完成操作，请刷新设备列表核实后重试。'
            : '无法进行身份验证，请确认已设置设备锁屏密码后重试。设备未移除。');
      }
    } finally {
      if (mounted) setState(() => _removing = null);
    }
  }

  @override
  Widget build(BuildContext context) {
    final devices = ref.watch(devicesProvider);
    return Scaffold(
      appBar: AppBar(title: const Text('设备管理'), actions: [
        IconButton(
            tooltip: '刷新设备',
            onPressed: _removing == null
                ? () => ref.invalidate(devicesProvider)
                : null,
            icon: const Icon(Icons.refresh)),
      ]),
      body: devices.when(
        loading: () => const Center(child: CircularProgressIndicator()),
        error: (_, __) =>
            AccountLoadError(onRetry: () => ref.invalidate(devicesProvider)),
        data: (data) => RefreshIndicator(
          onRefresh: () async {
            if (_removing == null) {
              ref.invalidate(devicesProvider);
              try {
                await ref.read(devicesProvider.future);
              } catch (_) {
                // Provider 的 error 状态展示重试入口。
              }
            }
          },
          child: ListView(
            physics: const AlwaysScrollableScrollPhysics(),
            padding: const EdgeInsets.all(YhSpace.page),
            children: [
              Text('${data.devices.length} / ${data.limit}',
                  style: const TextStyle(
                      fontSize: 40, fontWeight: FontWeight.w800)),
              Text('已登录设备 / 可用设备名额',
                  style: Theme.of(context).textTheme.labelSmall),
              const SizedBox(height: 24),
              if (_error != null)
                Padding(
                    padding: const EdgeInsets.only(bottom: 16),
                    child: Text(_error!,
                        style: const TextStyle(color: YhColors.coral))),
              if (data.devices.isEmpty)
                const Padding(
                    padding: EdgeInsets.symmetric(vertical: 32),
                    child: Center(child: Text('暂无已登录设备'))),
              for (final device in data.devices)
                Padding(
                  padding: const EdgeInsets.only(bottom: 12),
                  child: SettingsPanel(
                      child: Padding(
                          padding: const EdgeInsets.all(YhSpace.panel),
                          child: Column(
                              crossAxisAlignment: CrossAxisAlignment.start,
                              children: [
                                Row(children: [
                                  Icon(
                                      device.platform == 'IOS'
                                          ? Icons.phone_iphone
                                          : Icons.phone_android,
                                      color: YhColors.haze),
                                  const SizedBox(width: 12),
                                  Expanded(
                                      child: Text(device.displayName,
                                          style: Theme.of(context)
                                              .textTheme
                                              .titleLarge)),
                                  if (device.isCurrent)
                                    const Chip(
                                        label: Text('当前设备'),
                                        side: BorderSide.none),
                                ]),
                                const SizedBox(height: 12),
                                Text(
                                    '最近活跃 · ${accountDateLabel(device.lastSeen)}',
                                    style:
                                        Theme.of(context).textTheme.labelSmall),
                                const SizedBox(height: 4),
                                Text(
                                    '登录时间 · ${accountDateLabel(device.createdAt)}',
                                    style:
                                        Theme.of(context).textTheme.labelSmall),
                                if (!device.isCurrent)
                                  Align(
                                      alignment: Alignment.centerRight,
                                      child: TextButton(
                                        key: ValueKey(
                                            'remove-device-${device.id}'),
                                        onPressed: _removing == null
                                            ? () => _remove(device)
                                            : null,
                                        child: Text(
                                            _removing == device.id
                                                ? '正在移除…'
                                                : '移除设备',
                                            style: const TextStyle(
                                                color: YhColors.coral)),
                                      )),
                              ]))),
                ),
              const SizedBox(height: 12),
              Text('如需退出当前设备，请前往「账户与安全」。',
                  style: Theme.of(context).textTheme.labelSmall),
            ],
          ),
        ),
      ),
    );
  }
}
