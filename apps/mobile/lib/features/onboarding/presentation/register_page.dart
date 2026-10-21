import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../../app/router.dart';
import '../../../app/theme/tokens.dart';
import '../../../core/log/client_log.dart';
import '../../../core/network/api_client.dart';
import 'auth_failure.dart';
import 'auth_layout.dart';
import 'brand_scenes.dart';
import 'credential_rules.dart';
import 'onboarding_controller.dart';

/// 注册：账号 = 自己钱包的 USDT（TRC20）地址，密码自己设置。
class RegisterPage extends ConsumerStatefulWidget {
  const RegisterPage({super.key});
  @override
  ConsumerState<RegisterPage> createState() => _RegisterPageState();
}

class _RegisterPageState extends ConsumerState<RegisterPage> {
  final _address = TextEditingController();
  final _password = TextEditingController();
  final _again = TextEditingController();
  bool _busy = false;
  bool _taken = false;
  String? _addressError;
  String? _passwordError;
  String? _error;

  Future<void> _submit() async {
    final a = addressProblem(_address.text);
    final p = passwordProblem(_password.text, _again.text);
    setState(() {
      _addressError = a;
      _passwordError = p;
      _error = null;
      _taken = false;
    });
    if (a != null || p != null) return;
    setState(() => _busy = true);
    try {
      final invite = await ref.read(inviteHintProvider.future);
      await ref.read(sessionProvider.notifier).registerWithAddress(
          _address.text.trim(), _password.text,
          invite: invite);
      if (mounted) context.go(Routes.home);
    } on ApiException catch (e) {
      if (!mounted) return;
      setState(() {
        if (e.code == 40909) {
          _taken = true;
          _error = '这个地址已经注册过了，请直接登录。';
        } else if (e.code == 42901) {
          _error = '操作太频繁，请稍后再试';
        } else {
          _error = e.message;
        }
      });
    } catch (error, stack) {
      unawaited(ClientLog.exception('register', error, stack));
      if (mounted) setState(() => _error = authFailureCopy(error));
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
    _again.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return Scaffold(
      appBar:
          AppBar(title: const Text('注册'), backgroundColor: Colors.transparent),
      body: AuthLayout(
        child: AutofillGroup(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Text('创建账户', style: text.headlineMedium),
              const SizedBox(height: YhSpace.unit),
              Text('账号就是你的 USDT 地址，密码自己设置。注册后可领 24 小时免费试用。',
                  style: text.bodyMedium?.copyWith(color: YhColors.haze)),
              const SizedBox(height: YhSpace.page),
              const BrandSceneArt(scene: BrandScene.wallet, compact: true),
              const SizedBox(height: 8),
              Text(
                  '打开 TronLink 或 imToken，在资产页找到 USDT（TRC20），点复制，再回到这里粘贴。不要用交易所的充值地址。',
                  style: text.labelSmall),
              const SizedBox(height: YhSpace.page),
              TextField(
                controller: _address,
                autocorrect: false,
                enableSuggestions: false,
                style: const TextStyle(
                    fontFamily: YhFont.numeric, letterSpacing: 0.4),
                autofillHints: const [AutofillHints.username],
                decoration: InputDecoration(
                  labelText: 'USDT 地址（TRC20）',
                  hintText: 'T 开头，共 34 位',
                  errorText: _addressError,
                  suffixIcon: IconButton(
                      tooltip: '粘贴',
                      onPressed: _paste,
                      icon: const Icon(Icons.content_paste)),
                ),
              ),
              const SizedBox(height: YhSpace.unit * 2),
              TextField(
                controller: _password,
                obscureText: true,
                autofillHints: const [AutofillHints.newPassword],
                decoration: const InputDecoration(
                    labelText: '设置密码', helperText: '至少 8 位，包含字母和数字'),
              ),
              const SizedBox(height: YhSpace.unit * 2),
              TextField(
                controller: _again,
                obscureText: true,
                autofillHints: const [AutofillHints.newPassword],
                decoration: InputDecoration(
                    labelText: '再输一次密码', errorText: _passwordError),
                onSubmitted: (_) => _submit(),
              ),
              const SizedBox(height: YhSpace.page),
              Container(
                padding: const EdgeInsets.all(YhSpace.unit * 1.5),
                decoration: BoxDecoration(
                  color: YhColors.reef.withValues(alpha: 0.6),
                  borderRadius: BorderRadius.circular(YhRadius.listSelected),
                ),
                child: Text(
                  '请填写你自己钱包（如 TronLink、imToken）的地址，不要填交易所的充值地址。'
                  '忘记密码时，需要从这个地址转一笔很小的 TRX 来证明是你本人。',
                  style: text.labelSmall
                      ?.copyWith(color: YhColors.starlight, height: 1.6),
                ),
              ),
              if (_error != null) ...[
                const SizedBox(height: YhSpace.unit * 2),
                Text(_error!, style: const TextStyle(color: YhColors.coral)),
              ],
              const SizedBox(height: YhSpace.page),
              FilledButton(
                onPressed: _busy ? null : _submit,
                child: _busy
                    ? const SizedBox(
                        width: 20,
                        height: 20,
                        child: CircularProgressIndicator(strokeWidth: 2))
                    : const Text('注册并登录'),
              ),
              const SizedBox(height: YhSpace.unit),
              TextButton(
                onPressed: _busy
                    ? null
                    : () => context.pushReplacement(Routes.login,
                        extra: _taken ? _address.text.trim() : null),
                child: const Text('已有账户？去登录'),
              ),
            ],
          ),
        ),
      ),
    );
  }
}
