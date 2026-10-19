import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../../app/router.dart';
import '../../../app/theme/tokens.dart';
import '../../../core/network/api_client.dart';
import '../data/auth_api.dart';
import 'auth_layout.dart';
import 'credential_rules.dart';
import 'onboarding_controller.dart';

/// 登录：USDT 地址 + 密码。设备已满时选择移除一台。
class LoginPage extends ConsumerStatefulWidget {
  const LoginPage({super.key, this.initialAddress});
  final String? initialAddress;
  @override
  ConsumerState<LoginPage> createState() => _LoginPageState();
}

class _LoginPageState extends ConsumerState<LoginPage> {
  late final _address = TextEditingController(text: widget.initialAddress ?? '');
  final _password = TextEditingController();
  bool _busy = false;
  String? _addressError;
  String? _error;

  Future<void> _submit() async {
    final a = addressProblem(_address.text);
    setState(() {
      _addressError = a;
      _error = _password.text.isEmpty && a == null ? '请输入密码' : null;
    });
    if (a != null || _password.text.isEmpty) return;
    setState(() => _busy = true);
    try {
      await ref.read(sessionProvider.notifier).loginWithPassword(_address.text.trim(), _password.text);
      if (mounted) context.go(Routes.home);
    } on DeviceLimitReached catch (e) {
      if (!mounted) return;
      if (e.ticket.isEmpty || e.devices.isEmpty) {
        setState(() => _error = e.message);
        return;
      }
      final removeId =
          await pickDeviceToRemove(context, e.devices, limit: e.limit);
      if (!mounted) return;
      if (removeId == null || removeId.isEmpty) {
        setState(() => _error = e.message);
        return;
      }
      try {
        await ref.read(sessionProvider.notifier).replaceDevice(e.ticket, removeId);
        if (mounted) context.go(Routes.home);
      } on ApiException catch (e2) {
        if (mounted) setState(() => _error = e2.message);
      }
    } on ApiException catch (e) {
      if (!mounted) return;
      setState(() => _error = switch (e.code) {
            40113 => '地址或密码错误',
            42901 => '尝试次数过多，请稍后再试',
            40305 => '账户已停用，请联系客服',
            _ => e.message,
          });
    } catch (_) {
      if (mounted) setState(() => _error = '网络不可用，请稍后再试');
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _paste() async {
    final data = await Clipboard.getData(Clipboard.kTextPlain);
    final t = data?.text?.trim();
    if (t != null && t.isNotEmpty) _address.text = t;
  }

  @override
  void dispose() {
    _address.dispose();
    _password.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return Scaffold(
      appBar: AppBar(title: const Text('登录'), backgroundColor: Colors.transparent),
      body: AuthLayout(
        child: AutofillGroup(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Text('欢迎回来', style: text.headlineMedium),
              const SizedBox(height: YhSpace.page),
              TextField(
                controller: _address,
                autocorrect: false,
                enableSuggestions: false,
                style: const TextStyle(fontFamily: YhFont.numeric, letterSpacing: 0.4),
                autofillHints: const [AutofillHints.username],
                decoration: InputDecoration(
                  labelText: 'USDT 地址（TRC20）',
                  errorText: _addressError,
                  suffixIcon: IconButton(
                      tooltip: '粘贴', onPressed: _paste, icon: const Icon(Icons.content_paste)),
                ),
              ),
              const SizedBox(height: YhSpace.unit * 2),
              TextField(
                controller: _password,
                obscureText: true,
                autofillHints: const [AutofillHints.password],
                decoration: InputDecoration(labelText: '密码', errorText: _error),
                onSubmitted: (_) => _submit(),
              ),
              Align(
                alignment: Alignment.centerRight,
                child: TextButton(
                  onPressed: _busy ? null : () => context.push(Routes.resetPassword, extra: _address.text.trim()),
                  child: const Text('忘记密码？'),
                ),
              ),
              const SizedBox(height: YhSpace.unit),
              FilledButton(
                onPressed: _busy ? null : _submit,
                child: _busy
                    ? const SizedBox(width: 20, height: 20, child: CircularProgressIndicator(strokeWidth: 2))
                    : const Text('登录'),
              ),
              const SizedBox(height: YhSpace.unit),
              TextButton(
                onPressed: _busy ? null : () => context.pushReplacement(Routes.register),
                child: const Text('还没有账户？去注册'),
              ),
            ],
          ),
        ),
      ),
    );
  }
}
