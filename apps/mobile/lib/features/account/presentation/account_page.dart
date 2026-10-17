import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../../app/theme/tokens.dart';
import '../../../core/network/api_client.dart';
import '../../onboarding/presentation/credential_rules.dart';
import '../../connection/presentation/connection_controller.dart';
import '../../home/data/me_api.dart';
import '../../onboarding/presentation/onboarding_controller.dart';
import '../../settings/presentation/settings_widgets.dart';
import '../data/account_api.dart';

class AccountPage extends ConsumerStatefulWidget {
  const AccountPage({super.key});
  @override
  ConsumerState<AccountPage> createState() => _AccountPageState();
}

class _AccountPageState extends ConsumerState<AccountPage> {
  bool _signingOut = false;
  String? _error;

  Future<void> _logout(bool backedUp, {bool addressAccount = false}) async {
    final confirmed = await showDialog<bool>(
        context: context,
        builder: (context) => AlertDialog(
              title: const Text('退出当前账户？'),
              content: Text(addressAccount
                  ? '退出将断开当前连接并注销本设备。再次使用需要用你的地址和密码登录。'
                  : backedUp
                      ? '退出将断开当前连接并注销本设备。再次使用需要输入你保存的账户密钥。'
                      : '你还没有确认备份。退出后只能用账户密钥找回账户，请先确认已保存。退出还会断开当前连接。'),
              actions: [
                TextButton(
                    onPressed: () => Navigator.pop(context, false),
                    child: const Text('暂不退出')),
                TextButton(
                    onPressed: () => Navigator.pop(context, true),
                    child: Text(addressAccount || backedUp ? '确认退出' : '已保存密钥，退出')),
              ],
            ));
    if (confirmed != true || !mounted) return;
    setState(() {
      _signingOut = true;
      _error = null;
    });
    final engine = ref.read(vpnEngineProvider);
    final api = ref.read(accountApiProvider);
    final session = ref.read(sessionProvider.notifier);
    try {
      // 先取消仍在等待权限的连接请求，再等待断开；迟到的权限结果不能重新连接。
      ref.invalidate(connectionControllerProvider);
      // 服务器失败时保留本地登录供用户重试。
      await engine.disconnect();
      await api.logout();
      await session.signOut();
      if (!mounted) return;
      ref.invalidate(meProvider);
      ref.invalidate(devicesProvider);
    } catch (_) {
      if (!mounted) return;
      setState(() => _error = '退出未完成，请检查网络后重试。账户仍保留在本设备。');
    } finally {
      if (mounted) setState(() => _signingOut = false);
    }
  }

  Future<void> _copyAccount(String account, {String label = '账户号'}) async {
    try {
      await Clipboard.setData(ClipboardData(text: account));
      if (mounted) {
        ScaffoldMessenger.of(context)
            .showSnackBar(SnackBar(content: Text('$label已复制')));
      }
    } catch (_) {
      if (mounted) {
        ScaffoldMessenger.of(context)
            .showSnackBar(SnackBar(content: Text('复制失败，请长按$label复制')));
      }
    }
  }

  Future<void> _changePassword() async {
    final changed = await showDialog<bool>(
        context: context, builder: (_) => const _ChangePasswordDialog());
    if (changed == true && mounted) {
      ScaffoldMessenger.of(context)
          .showSnackBar(const SnackBar(content: Text('密码已修改')));
    }
  }

  Widget _addressAccount(
      BuildContext context, Map<String, dynamic> data, String address) {
    final text = Theme.of(context).textTheme;
    return ListView(padding: const EdgeInsets.all(YhSpace.page), children: [
      SettingsPanel(
          child: Padding(
              padding: const EdgeInsets.all(YhSpace.panel),
              child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text('登录地址（USDT · TRC20）', style: text.labelSmall),
                    const SizedBox(height: 8),
                    Row(children: [
                      Expanded(
                          child: SelectableText(address,
                              style: const TextStyle(
                                  fontFamily: YhFont.numeric,
                                  fontSize: 16,
                                  fontWeight: FontWeight.w700,
                                  letterSpacing: 0.4))),
                      IconButton(
                          tooltip: '复制地址',
                          onPressed: () =>
                              _copyAccount(address, label: '地址'),
                          icon: const Icon(Icons.copy_outlined)),
                    ]),
                    const SizedBox(height: 8),
                    Text(
                        data['status'] == 'ACTIVE'
                            ? '账户状态 · 正常'
                            : '账户状态 · 暂停使用',
                        style: text.labelSmall),
                  ]))),
      const SizedBox(height: 24),
      SettingsPanel(
          child: Padding(
              padding: const EdgeInsets.all(YhSpace.panel),
              child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    const Icon(Icons.lock_outline, color: YhColors.starlight),
                    const SizedBox(height: 12),
                    Text('登录密码', style: text.titleLarge),
                    const SizedBox(height: 8),
                    const Text('忘记密码时，可以从这个地址转一笔很小的 TRX 来重设密码。'),
                    const SizedBox(height: 12),
                    OutlinedButton(
                        onPressed: _changePassword,
                        child: const Text('修改密码')),
                    const SizedBox(height: 8),
                    Text('请勿向任何人提供密码，包括自称客服的人。',
                        style:
                            text.bodyMedium?.copyWith(color: YhColors.haze)),
                  ]))),
      const SizedBox(height: 32),
      if (_error != null)
        Padding(
            padding: const EdgeInsets.only(bottom: 16),
            child: Text(_error!, style: const TextStyle(color: YhColors.coral))),
      OutlinedButton.icon(
        onPressed: _signingOut
            ? null
            : () => _logout(true, addressAccount: true),
        icon: _signingOut
            ? const SizedBox(
                width: 18,
                height: 18,
                child: CircularProgressIndicator(strokeWidth: 2))
            : const Icon(Icons.logout),
        label: Text(_signingOut ? '正在退出…' : '退出当前账户'),
        style: OutlinedButton.styleFrom(
            foregroundColor: YhColors.coral,
            minimumSize: const Size.fromHeight(52)),
      ),
    ]);
  }

  @override
  Widget build(BuildContext context) {
    final me = ref.watch(meProvider);
    return Scaffold(
      appBar: AppBar(title: const Text('账户与安全')),
      body: me.when(
        loading: () => const Center(child: CircularProgressIndicator()),
        error: (_, __) =>
            AccountLoadError(onRetry: () => ref.invalidate(meProvider)),
        data: (data) {
          final account = data['account_no'] as String? ?? '';
          final backedUp = data['backup_acknowledged'] == true;
          final loginAddress = data['login_address'] as String? ?? '';
          if (loginAddress.isNotEmpty) {
            return _addressAccount(context, data, loginAddress);
          }
          return ListView(
              padding: const EdgeInsets.all(YhSpace.page),
              children: [
                SettingsPanel(
                    child: Padding(
                        padding: const EdgeInsets.all(YhSpace.panel),
                        child: Column(
                            crossAxisAlignment: CrossAxisAlignment.start,
                            children: [
                              Text('我的账户号',
                                  style:
                                      Theme.of(context).textTheme.labelSmall),
                              const SizedBox(height: 8),
                              Row(children: [
                                Expanded(
                                    child: SelectableText(
                                        account.isEmpty ? '—' : account,
                                        style: const TextStyle(
                                            fontSize: 28,
                                            fontWeight: FontWeight.w800,
                                            letterSpacing: 1))),
                                IconButton(
                                    tooltip: '复制账户号',
                                    onPressed: account.isEmpty
                                        ? null
                                        : () => _copyAccount(account),
                                    icon: const Icon(Icons.copy_outlined)),
                              ]),
                              const SizedBox(height: 8),
                              Text(
                                  data['status'] == 'ACTIVE'
                                      ? '账户状态 · 正常'
                                      : '账户状态 · 暂停使用',
                                  style:
                                      Theme.of(context).textTheme.labelSmall),
                            ]))),
                const SizedBox(height: 24),
                SettingsPanel(
                    child: Padding(
                        padding: const EdgeInsets.all(YhSpace.panel),
                        child: Column(
                            crossAxisAlignment: CrossAxisAlignment.start,
                            children: [
                              const Icon(Icons.key_outlined,
                                  color: YhColors.starlight),
                              const SizedBox(height: 12),
                              Text(backedUp ? '已确认保存账户密钥' : '请妥善保存账户密钥',
                                  style:
                                      Theme.of(context).textTheme.titleLarge),
                              const SizedBox(height: 8),
                              const Text(
                                  '账户密钥在创建账户时展示，用于在新设备上找回账户。当前版本不会再次显示或复制密钥。'),
                              const SizedBox(height: 12),
                              Text('请勿向任何人提供账户密钥，包括自称客服的人。',
                                  style: Theme.of(context)
                                      .textTheme
                                      .bodyMedium
                                      ?.copyWith(color: YhColors.haze)),
                            ]))),
                const SizedBox(height: 32),
                if (_error != null)
                  Padding(
                      padding: const EdgeInsets.only(bottom: 16),
                      child: Text(_error!,
                          style: const TextStyle(color: YhColors.coral))),
                OutlinedButton.icon(
                  onPressed: _signingOut ? null : () => _logout(backedUp),
                  icon: _signingOut
                      ? const SizedBox(
                          width: 18,
                          height: 18,
                          child: CircularProgressIndicator(strokeWidth: 2))
                      : const Icon(Icons.logout),
                  label: Text(_signingOut ? '正在退出…' : '退出当前账户'),
                  style: OutlinedButton.styleFrom(
                      foregroundColor: YhColors.coral,
                      minimumSize: const Size.fromHeight(52)),
                ),
              ]);
        },
      ),
    );
  }
}

class _ChangePasswordDialog extends ConsumerStatefulWidget {
  const _ChangePasswordDialog();
  @override
  ConsumerState<_ChangePasswordDialog> createState() =>
      _ChangePasswordDialogState();
}

class _ChangePasswordDialogState extends ConsumerState<_ChangePasswordDialog> {
  final _old = TextEditingController();
  final _new = TextEditingController();
  final _again = TextEditingController();
  bool _busy = false;
  String? _error;

  Future<void> _submit() async {
    final problem = passwordProblem(_new.text, _again.text);
    if (problem != null) {
      setState(() => _error = problem);
      return;
    }
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      await ref.read(authApiProvider).changePassword(_old.text, _new.text);
      if (mounted) Navigator.pop(context, true);
    } on ApiException catch (e) {
      if (mounted) {
        setState(() => _error = e.code == 40113 ? '原密码不对' : e.message);
      }
    } catch (_) {
      if (mounted) setState(() => _error = '网络不可用，请稍后再试');
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  void dispose() {
    _old.dispose();
    _new.dispose();
    _again.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return AlertDialog(
      title: const Text('修改密码'),
      content: Column(mainAxisSize: MainAxisSize.min, children: [
        TextField(
            controller: _old,
            obscureText: true,
            decoration: const InputDecoration(labelText: '原密码')),
        const SizedBox(height: 8),
        TextField(
            controller: _new,
            obscureText: true,
            decoration: const InputDecoration(
                labelText: '新密码', helperText: '至少 8 位，包含字母和数字')),
        const SizedBox(height: 8),
        TextField(
            controller: _again,
            obscureText: true,
            decoration: InputDecoration(labelText: '再输一次新密码', errorText: _error),
            onSubmitted: (_) => _submit()),
      ]),
      actions: [
        TextButton(
            onPressed: _busy ? null : () => Navigator.pop(context, false),
            child: const Text('取消')),
        TextButton(
            onPressed: _busy ? null : _submit,
            child: Text(_busy ? '正在保存…' : '保存')),
      ],
    );
  }
}
