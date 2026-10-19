import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../../app/providers.dart';
import '../../../app/router.dart';
import '../../../app/theme/tokens.dart';
import '../../account/presentation/support_sheet.dart';
import '../../account/data/account_api.dart';
import '../../home/data/me_api.dart';
import 'settings_widgets.dart';

class SettingsPage extends ConsumerWidget {
  const SettingsPage({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final me = ref.watch(meProvider);
    return Scaffold(
      appBar: AppBar(title: const Text('设置')),
      body: me.when(
        loading: () => const Center(child: CircularProgressIndicator()),
        error: (_, __) =>
            AccountLoadError(onRetry: () => ref.invalidate(meProvider)),
        data: (data) {
          final entitlement = (data['entitlement'] as Map?) ?? const {};
          final backedUp = data['backup_acknowledged'] == true;
          return RefreshIndicator(
            onRefresh: () async {
              ref.invalidate(meProvider);
              try {
                await ref.read(meProvider.future);
              } catch (_) {
                // Provider 的 error 状态展示重试入口。
              }
            },
            child: ListView(
              physics: const AlwaysScrollableScrollPhysics(),
              padding:
                  const EdgeInsets.fromLTRB(YhSpace.page, 8, YhSpace.page, 32),
              children: [
                SettingsPanel(
                    child: Padding(
                  padding: const EdgeInsets.all(YhSpace.panel),
                  child: Row(children: [
                    const CircleAvatar(
                        radius: 25,
                        backgroundColor: YhColors.reef,
                        child: Icon(Icons.person_outline,
                            color: YhColors.moonlight)),
                    const SizedBox(width: 16),
                    Expanded(
                        child: Column(
                            crossAxisAlignment: CrossAxisAlignment.start,
                            children: [
                          Text('宇海VPN',
                              style: Theme.of(context).textTheme.titleLarge),
                          const SizedBox(height: 4),
                          Text('${data['account_no'] ?? '—'}',
                              style: const TextStyle(
                                  fontWeight: FontWeight.w800,
                                  letterSpacing: 1)),
                        ])),
                  ]),
                )),
                const SizedBox(height: 24),
                SettingsPanel(
                    child: Column(children: [
                  SettingsRow(
                      icon: Icons.shield_outlined,
                      title: '账户与安全',
                      subtitle: backedUp ? '已确认保存账户密钥' : '请确认账户密钥已妥善保存',
                      onTap: () => context.push(Routes.account)),
                  const Divider(height: 1),
                  SettingsRow(
                      icon: Icons.devices_outlined,
                      title: '设备管理',
                      subtitle:
                          '${entitlement['device_count'] ?? 0} / ${entitlement['device_limit'] ?? 0} 台设备',
                      onTap: () => context.push(Routes.devices)),
                ])),
                const SizedBox(height: 16),
                SettingsPanel(
                    child: Column(children: [
                  SettingsRow(
                      icon: Icons.account_balance_wallet_outlined,
                      title: '余额与流水',
                      subtitle: '${accountBalanceLabel(data['balance'])} USDT',
                      onTap: () => context.push(Routes.balance)),
                  const Divider(height: 1),
                  SettingsRow(
                      icon: Icons.bar_chart_rounded,
                      title: '流量明细',
                      subtitle: '每日用量与设备分布',
                      onTap: () => context.push(Routes.traffic)),
                  const Divider(height: 1),
                  SettingsRow(
                      icon: Icons.receipt_long_outlined,
                      title: '订单记录',
                      onTap: () => context.push(Routes.orders)),
                  const Divider(height: 1),
                  SettingsRow(
                      icon: Icons.confirmation_number_outlined,
                      title: '兑换码',
                      subtitle: '周卡、月卡、年卡',
                      onTap: () => context.push(Routes.redeem)),
                  const Divider(height: 1),
                  SettingsRow(
                      icon: Icons.card_giftcard_outlined,
                      title: '邀请好友',
                      subtitle: '查看邀请码与奖励记录',
                      onTap: () => context.push(Routes.invite)),
                ])),
                const SizedBox(height: 16),
                SettingsPanel(
                    child: Column(children: [
                  SettingsRow(
                      icon: Icons.tune_rounded,
                      title: '连接设置',
                      subtitle: '协议偏好与连接动画',
                      onTap: () => context.push(Routes.connection)),
                  const Divider(height: 1),
                  const SettingsRow(
                      icon: Icons.language_outlined,
                      title: '语言',
                      subtitle: '简体中文'),
                  const Divider(height: 1),
                  SettingsRow(
                      icon: Icons.info_outline,
                      title: '关于宇海VPN',
                      subtitle: appVersion,
                      onTap: () => context.push(Routes.about)),
                  const Divider(height: 1),
                  SettingsRow(
                      icon: Icons.support_agent_outlined,
                      title: '联系客服',
                      subtitle: 'Telegram',
                      onTap: () => showSupportSheet(context)),
                ])),
              ],
            ),
          );
        },
      ),
    );
  }
}
