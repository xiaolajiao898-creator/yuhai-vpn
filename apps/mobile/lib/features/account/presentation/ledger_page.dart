import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../data/ledger_api.dart';

class LedgerPage extends ConsumerStatefulWidget {
  const LedgerPage({super.key});
  @override
  ConsumerState<LedgerPage> createState() => _LedgerPageState();
}

class _LedgerPageState extends ConsumerState<LedgerPage> {
  bool _entitlements = false, _loading = false;
  String? _cursor, _error;
  List<LedgerEntry> _items = [];
  int _generation = 0;

  @override
  void initState() {
    super.initState();
    _load(reset: true);
  }

  Future<void> _load({bool reset = false}) async {
    if (_loading && !reset) return;
    final generation = reset ? ++_generation : _generation;
    setState(() {
      _loading = true;
      _error = null;
      if (reset) { _items = []; _cursor = null; }
    });
    try {
      final batch = await ref.read(ledgerApiProvider).list(_entitlements, cursor: reset ? null : _cursor);
      if (!mounted || generation != _generation) return;
      setState(() {
        final known = _items.map((e) => e.id).toSet();
        _items = [..._items, ...batch.items.where((e) => known.add(e.id))];
        _cursor = batch.nextCursor;
        _loading = false;
      });
    } catch (_) {
      if (!mounted || generation != _generation) return;
      setState(() { _loading = false; _error = '暂时无法加载，请重试'; });
    }
  }

  @override
  Widget build(BuildContext context) => Scaffold(
    appBar: AppBar(title: const Text('账户账单')),
    body: Column(children: [
      Padding(padding: const EdgeInsets.all(16), child: SegmentedButton<bool>(
        segments: const [ButtonSegment(value: false, label: Text('余额流水')), ButtonSegment(value: true, label: Text('权益记录'))],
        selected: {_entitlements},
        onSelectionChanged: (selection) { setState(() => _entitlements = selection.single); _load(reset: true); },
      )),
      Expanded(child: RefreshIndicator(
        onRefresh: () => _load(reset: true),
        child: ListView.builder(
          physics: const AlwaysScrollableScrollPhysics(),
          padding: const EdgeInsets.fromLTRB(16, 0, 16, 32),
          itemCount: _items.length + 1,
          itemBuilder: (context, i) {
            if (i == _items.length) {
              if (_loading) return const Padding(padding: EdgeInsets.all(24), child: Center(child: CircularProgressIndicator()));
              if (_error != null) return TextButton(onPressed: () => _load(reset: _items.isEmpty), child: Text(_error!));
              if (_items.isEmpty) return const Padding(padding: EdgeInsets.all(32), child: Center(child: Text('暂无记录')));
              if (_cursor != null) return TextButton(onPressed: _load, child: const Text('加载更多'));
              return const Padding(padding: EdgeInsets.all(24), child: Center(child: Text('已显示全部记录')));
            }
            final e = _items[i];
            final date = e.createdAt.toLocal().toString().split('.').first;
            return Card(child: Padding(padding: const EdgeInsets.all(16), child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
              Text(_labels[e.kind] ?? '账户变动', style: Theme.of(context).textTheme.titleMedium),
              const SizedBox(height: 8),
              Text(_value(e), style: const TextStyle(fontWeight: FontWeight.w800, fontSize: 22)),
              const SizedBox(height: 8),
              Text(date, style: Theme.of(context).textTheme.bodySmall),
              if (e.note?.isNotEmpty ?? false) Text(e.note!),
            ])));
          },
        ),
      )),
    ]),
  );
}

const _labels = {'DEPOSIT': '充值到账', 'PURCHASE': '购买套餐', 'RENEW': '套餐续费', 'TIER_CHANGE': '套餐变更', 'TRAFFIC_PACK': '流量包', 'DEVICE_ADDON': '设备加购', 'INVITE_REWARD': '邀请奖励', 'EXCEPTION_CREDIT': '异常付款入账', 'FULFILL_FALLBACK': '转入余额', 'ADMIN_CREDIT': '人工入账', 'ADMIN_DEBIT': '人工扣款', 'ADMIN_ADJUST': '人工调整', 'EXPIRE': '权益到期', 'REVOKE': '权益撤销', 'PERIOD_RESET': '周期重置', 'TRAFFIC_CONSUME_OVERFLOW': '流量扣减'};
String _value(LedgerEntry e) {
  if (e.amount != null) {
    var amount = e.amount!;
    if (amount.contains('.')) amount = amount.replaceFirst(RegExp(r'0+$'), '').replaceFirst(RegExp(r'\.$'), '');
    return '${amount.startsWith('-') ? '' : '+'}$amount USDT';
  }
  final parts = <String>[];
  if ((e.seconds ?? 0) != 0) parts.add('${e.seconds! > 0 ? '+' : ''}${e.seconds} 秒');
  if ((e.bytes ?? 0) != 0) parts.add('${e.bytes! > 0 ? '+' : ''}${e.bytes} 字节');
  if ((e.devices ?? 0) != 0) parts.add('${e.devices! > 0 ? '+' : ''}${e.devices} 台设备');
  return parts.isEmpty ? '权益状态更新' : parts.join(' · ');
}
