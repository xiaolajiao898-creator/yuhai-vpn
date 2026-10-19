import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../../app/router.dart';
import '../../../app/theme/tokens.dart';
import '../../../core/network/api_client.dart';
import '../data/auth_api.dart';
import 'auth_layout.dart';
import 'brand_scenes.dart';
import 'credential_rules.dart';
import 'onboarding_controller.dart';

/// 忘记密码：从注册地址向官方地址转一笔精确金额的 TRX，链上确认后设置新密码。
/// 金额每次不同（1 TRX + 随机零头），这样别人即使同时为你的地址发起找回，也认领不了你的转账。
class ResetPasswordPage extends ConsumerStatefulWidget {
  const ResetPasswordPage({super.key, this.initialAddress});
  final String? initialAddress;
  @override
  ConsumerState<ResetPasswordPage> createState() => _ResetPasswordPageState();
}

class _ResetPasswordPageState extends ConsumerState<ResetPasswordPage> {
  late final _address = TextEditingController(text: widget.initialAddress ?? '');
  final _password = TextEditingController();
  final _again = TextEditingController();
  ResetChallenge? _challenge;
  bool _busy = false;
  bool _waiting = false;
  String? _error;
  String? _status;
  Timer? _poll;
  DateTime? _waitStarted;

  static const _pollEvery = Duration(seconds: 6);
  static const _giveUpAfter = Duration(minutes: 10);

  Future<void> _start() async {
    final a = addressProblem(_address.text);
    if (a != null) {
      setState(() => _error = a);
      return;
    }
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final c = await ref.read(authApiProvider).startPasswordReset(_address.text.trim());
      if (mounted) setState(() => _challenge = c);
    } on ApiException catch (e) {
      if (!mounted) return;
      setState(() => _error = switch (e.code) {
            40401 => '这个地址还没有注册',
            42901 => '找回请求太多，请稍后再试',
            50303 => '找回密码暂未开放，请联系客服',
            _ => e.message,
          });
    } catch (_) {
      if (mounted) setState(() => _error = '网络不可用，请稍后再试');
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  void _beginWaiting() {
    final p = passwordProblem(_password.text, _again.text);
    if (p != null) {
      setState(() => _error = p);
      return;
    }
    setState(() {
      _error = null;
      _waiting = true;
      _status = '正在查询链上转账…';
      _waitStarted = DateTime.now();
    });
    unawaited(_tryComplete());
  }

  void _stopWaiting([String? message]) {
    _poll?.cancel();
    _poll = null;
    if (mounted) {
      setState(() {
        _waiting = false;
        _status = null;
        _error = message;
      });
    }
  }

  Future<void> _tryComplete() async {
    final c = _challenge;
    if (c == null || !_waiting) return;
    try {
      await ref.read(sessionProvider.notifier).completePasswordReset(c.id, _password.text);
      _poll?.cancel();
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('密码已重设，已登录')));
      context.go(Routes.home);
      return;
    } on ApiException catch (e) {
      if (e.code == 40910) {
        final waited = DateTime.now().difference(_waitStarted ?? DateTime.now());
        if (waited > _giveUpAfter) {
          _stopWaiting('等了很久还没查到转账。请确认是从注册地址转出、金额完全一致，然后再点一次。');
          return;
        }
        if (mounted) setState(() => _status = '还没查到转账，链上确认一般需要 1 分钟左右，正在自动重试…');
        _poll = Timer(_pollEvery, () => unawaited(_tryComplete()));
        return;
      }
      _stopWaiting(switch (e.code) {
        40401 => '这次找回已过期，请返回重新发起',
        40901 => e.message,
        50303 => '链上查询暂时不可用，请稍后再点一次',
        _ => e.message,
      });
    } catch (_) {
      _stopWaiting('网络不可用，请稍后再点一次');
    }
  }

  Future<void> _copy(String value, String label) async {
    try {
      await Clipboard.setData(ClipboardData(text: value));
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text('$label已复制')));
      }
    } catch (_) {}
  }

  @override
  void dispose() {
    _poll?.cancel();
    _address.dispose();
    _password.dispose();
    _again.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final c = _challenge;
    return Scaffold(
      appBar: AppBar(title: const Text('找回密码'), backgroundColor: Colors.transparent),
      body: AuthLayout(child: c == null ? _stepAddress(context) : _stepTransfer(context, c)),
    );
  }

  Widget _stepAddress(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
      Text('输入注册地址', style: text.headlineMedium),
      const SizedBox(height: YhSpace.unit),
      Text('下一步会告诉你要转多少 TRX、转到哪里。从注册地址转出，用来证明是你本人。',
          style: text.bodyMedium?.copyWith(color: YhColors.haze)),
      const SizedBox(height: YhSpace.page),
      const BrandSceneArt(scene: BrandScene.recover, compact: true),
      const SizedBox(height: YhSpace.page),
      TextField(
        controller: _address,
        autocorrect: false,
        enableSuggestions: false,
        style: const TextStyle(fontFamily: YhFont.numeric, letterSpacing: 0.4),
        decoration: InputDecoration(labelText: 'USDT 地址（TRC20）', errorText: _error),
        onSubmitted: (_) => _start(),
      ),
      const SizedBox(height: YhSpace.page),
      FilledButton(
        onPressed: _busy ? null : _start,
        child: _busy
            ? const SizedBox(width: 20, height: 20, child: CircularProgressIndicator(strokeWidth: 2))
            : const Text('下一步'),
      ),
    ]);
  }

  Widget _stepTransfer(BuildContext context, ResetChallenge c) {
    final text = Theme.of(context).textTheme;
    final expires = c.expiresAt;
    String two(int n) => n.toString().padLeft(2, '0');
    return Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
      Text('转账验证', style: text.headlineMedium),
      const SizedBox(height: YhSpace.unit),
      Text('请用注册地址所在的钱包，转出下面这个金额。金额要完全一样，多一位少一位都查不到。',
          style: text.bodyMedium?.copyWith(color: YhColors.haze, height: 1.6)),
      const SizedBox(height: YhSpace.page),
      _CopyRow(label: '转账金额（TRX）', value: c.amount, big: true, onCopy: () => _copy(c.amount, '金额')),
      const SizedBox(height: YhSpace.unit * 2),
      _CopyRow(label: '收款地址（TRON）', value: c.payTo, onCopy: () => _copy(c.payTo, '收款地址')),
      const SizedBox(height: YhSpace.unit),
      Text('请在 ${two(expires.hour)}:${two(expires.minute)} 前转账。只转 TRX，不要转 USDT。',
          style: text.labelSmall?.copyWith(color: YhColors.starlight)),
      const SizedBox(height: YhSpace.page),
      TextField(
        controller: _password,
        obscureText: true,
        enabled: !_waiting,
        decoration: const InputDecoration(labelText: '新密码', helperText: '至少 8 位，包含字母和数字'),
      ),
      const SizedBox(height: YhSpace.unit * 2),
      TextField(
        controller: _again,
        obscureText: true,
        enabled: !_waiting,
        decoration: const InputDecoration(labelText: '再输一次新密码'),
      ),
      if (_status != null) ...[
        const SizedBox(height: YhSpace.unit * 2),
        Row(children: [
          const SizedBox(width: 16, height: 16, child: CircularProgressIndicator(strokeWidth: 2)),
          const SizedBox(width: YhSpace.unit),
          Expanded(child: Text(_status!, style: text.bodyMedium)),
        ]),
      ],
      if (_error != null) ...[
        const SizedBox(height: YhSpace.unit * 2),
        Text(_error!, style: const TextStyle(color: YhColors.coral)),
      ],
      const SizedBox(height: YhSpace.page),
      if (!_waiting)
        FilledButton(onPressed: _beginWaiting, child: const Text('我已转账，重设密码'))
      else
        OutlinedButton(onPressed: () => _stopWaiting(), child: const Text('停止等待')),
      const SizedBox(height: YhSpace.unit),
      TextButton(
        onPressed: _waiting ? null : () => setState(() {
          _challenge = null;
          _error = null;
        }),
        child: const Text('重新发起'),
      ),
    ]);
  }
}

class _CopyRow extends StatelessWidget {
  const _CopyRow({required this.label, required this.value, required this.onCopy, this.big = false});
  final String label;
  final String value;
  final VoidCallback onCopy;
  final bool big;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return Container(
      padding: const EdgeInsets.all(YhSpace.unit * 1.5),
      decoration: BoxDecoration(
        color: YhColors.reef.withValues(alpha: 0.6),
        borderRadius: BorderRadius.circular(YhRadius.listSelected),
      ),
      child: Row(children: [
        Expanded(
          child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            Text(label, style: text.labelSmall?.copyWith(color: YhColors.haze)),
            const SizedBox(height: 4),
            SelectableText(value,
                style: TextStyle(
                    fontFamily: YhFont.numeric,
                    fontSize: big ? 26 : 14,
                    fontWeight: big ? FontWeight.w800 : FontWeight.w600,
                    color: big ? YhColors.starlight : YhColors.moonlight,
                    letterSpacing: big ? 0.5 : 0.2)),
          ]),
        ),
        IconButton(tooltip: '复制', onPressed: onCopy, icon: const Icon(Icons.copy_outlined)),
      ]),
    );
  }
}
