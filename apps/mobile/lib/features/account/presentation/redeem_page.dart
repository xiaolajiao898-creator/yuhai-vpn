import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../../app/theme/tokens.dart';
import '../../../core/network/api_client.dart';
import '../../home/data/me_api.dart';
import '../data/redeem_api.dart';

/// 兑换周卡 / 月卡 / 年卡。成功后刷新 /v1/me，首页立刻显示套餐。
class RedeemPage extends ConsumerStatefulWidget {
  const RedeemPage({super.key});

  @override
  ConsumerState<RedeemPage> createState() => _RedeemPageState();
}

class _RedeemPageState extends ConsumerState<RedeemPage> {
  final _code = TextEditingController();
  var _busy = false;
  String? _error;
  RedeemResult? _done;

  @override
  void dispose() {
    _code.dispose();
    super.dispose();
  }

  Future<void> _submit() async {
    final code = _code.text.trim();
    if (code.isEmpty || _busy) return;
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final result = await ref.read(redeemApiProvider).redeem(code);
      ref.invalidate(meProvider);
      try {
        await ref.read(meProvider.future);
      } catch (_) {
        // 兑换已经成功。账户信息刷新失败时仍展示开通结果。
      }
      if (!mounted) return;
      setState(() {
        _done = result;
        _code.clear();
      });
    } on ApiException catch (e) {
      if (e.message == '已使用') {
        ref.invalidate(meProvider);
      }
      if (!mounted) return;
      setState(() => _error = e.message);
    } catch (_) {
      if (!mounted) return;
      setState(() => _error = '兑换失败，请稍后再试');
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    final done = _done;
    return Scaffold(
      appBar: AppBar(title: const Text('兑换码')),
      body: ListView(
        padding: const EdgeInsets.fromLTRB(YhSpace.page, 16, YhSpace.page, 32),
        children: [
          Text('输入周卡、月卡或年卡的兑换码。', style: text.bodyMedium),
          const SizedBox(height: 8),
          Text('如果已经有套餐，天数会加在当前到期日之后，不会把更高等级或更长的剩余时间改短。',
              style: text.labelSmall),
          const SizedBox(height: 20),
          TextField(
            controller: _code,
            textCapitalization: TextCapitalization.characters,
            autocorrect: false,
            enableSuggestions: false,
            decoration: const InputDecoration(
              labelText: '兑换码',
              hintText: 'ABCD-EFGH-JKLM-NPQR',
            ),
            onSubmitted: (_) => _submit(),
          ),
          const SizedBox(height: 16),
          FilledButton(
            onPressed: _busy ? null : _submit,
            child: Text(_busy ? '正在兑换…' : '兑换'),
          ),
          if (_error != null) ...[
            const SizedBox(height: 16),
            Text(_error!, style: text.bodyMedium?.copyWith(color: YhColors.danger)),
          ],
          if (done != null) ...[
            const SizedBox(height: 24),
            Text(
              done.extended
                  ? '已在当前到期时间上增加 ${done.days} 天'
                  : '已开通${done.cardName}',
              style: text.titleLarge,
            ),
            const SizedBox(height: 8),
            Text(
              'L${done.tier} · ${done.deviceLimit} 台设备 · ${done.unlimited ? "不限流量" : "按卡面流量"}'
              '${done.expireAt == null ? "" : " · 到期 ${done.expireAt!.toLocal().toString().substring(0, 16)}"}',
              style: text.bodyMedium,
            ),
          ],
        ],
      ),
    );
  }
}
