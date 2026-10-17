import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../../app/theme/tokens.dart';
import '../../account/data/account_api.dart';
import '../../settings/presentation/settings_widgets.dart';
import '../data/invite_api.dart';
import 'reward_format.dart';

class RewardsPage extends ConsumerStatefulWidget {
  const RewardsPage({super.key});
  @override
  ConsumerState<RewardsPage> createState() => _RewardsPageState();
}

class _RewardsPageState extends ConsumerState<RewardsPage> {
  final List<InviteReward> _items = [];
  String? _cursor;
  bool _loading = false, _error = false;
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
      _error = false;
      if (reset) {
        _items.clear();
        _cursor = null;
      }
    });
    try {
      final (items, cursor) =
          await ref.read(inviteApiProvider).rewards(cursor: _cursor);
      if (!mounted || generation != _generation) return;
      setState(() {
        final ids = _items.map((item) => item.id).toSet();
        _items.addAll(items.where((item) => ids.add(item.id)));
        _cursor = cursor;
        _loading = false;
      });
    } catch (_) {
      if (!mounted || generation != _generation) return;
      setState(() {
        _loading = false;
        _error = true;
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    final now = ref.watch(inviteNowProvider)();
    final info = ref.watch(inviteInfoProvider).valueOrNull;
    return Scaffold(
      appBar: AppBar(title: const Text('奖励记录'), actions: [
        IconButton(
            tooltip: '刷新奖励',
            onPressed: () => _load(reset: true),
            icon: const Icon(Icons.refresh)),
      ]),
      body: RefreshIndicator(
        onRefresh: () => _load(reset: true),
        child: ListView.builder(
          physics: const AlwaysScrollableScrollPhysics(),
          padding: const EdgeInsets.all(YhSpace.page),
          itemCount: _items.length + 2,
          itemBuilder: (context, index) {
            if (index == 0) {
              return Padding(
                  padding: const EdgeInsets.only(bottom: 20),
                  child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        if (info != null && !info.rewardsEnabled) ...[
                          Text('奖励活动暂未开放，历史记录仍可查看。',
                              style: Theme.of(context)
                                  .textTheme
                                  .bodyMedium
                                  ?.copyWith(color: YhColors.starlight)),
                          const SizedBox(height: 10),
                        ],
                        Text('冻结中和审核中的奖励尚未到账，请以已发放记录和账户流水为准。',
                            style: Theme.of(context)
                                .textTheme
                                .bodyMedium
                                ?.copyWith(color: YhColors.haze)),
                      ]));
            }
            if (index == _items.length + 1) {
              if (_loading) {
                return const Center(
                    child: Padding(
                        padding: EdgeInsets.all(24),
                        child: CircularProgressIndicator()));
              }
              if (_error) {
                return TextButton(
                    onPressed: () => _load(reset: _items.isEmpty),
                    child: const Text('加载失败，点击重试'));
              }
              if (_cursor != null) {
                return TextButton(onPressed: _load, child: const Text('加载更多'));
              }
              if (_items.isEmpty) {
                return SettingsPanel(
                    child: Padding(
                        padding: const EdgeInsets.all(32),
                        child: Column(children: [
                          const Icon(Icons.card_giftcard_outlined,
                              color: YhColors.haze, size: 36),
                          const SizedBox(height: 12),
                          const Text('暂无奖励记录'),
                          const SizedBox(height: 8),
                          Text('符合活动规则后，奖励处理进度会显示在这里。',
                              textAlign: TextAlign.center,
                              style: Theme.of(context).textTheme.labelSmall),
                        ])));
              }
              return const Padding(
                  padding: EdgeInsets.all(24),
                  child: Center(child: Text('已显示全部记录')));
            }
            final reward = _items[index - 1];
            final color = switch (reward.status) {
              'RELEASED' => YhColors.glow,
              'PENDING' => YhColors.starlight,
              'REJECTED' => YhColors.haze,
              _ => YhColors.moonlight,
            };
            return Padding(
                padding: const EdgeInsets.only(bottom: 14),
                child: SettingsPanel(
                    child: Padding(
                  key: ValueKey('invite-reward-${reward.id}'),
                  padding: const EdgeInsets.all(YhSpace.panel),
                  child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Row(children: [
                          Expanded(
                              child: Text(inviteRewardValue(reward),
                                  style: const TextStyle(
                                      fontFamily: 'Arial Rounded MT Bold',
                                      fontFamilyFallback: [
                                        'SF Pro Rounded',
                                        YhFont.numeric
                                      ],
                                      fontSize: 24,
                                      fontWeight: FontWeight.w800))),
                          const SizedBox(width: 8),
                          Container(
                              padding: const EdgeInsets.symmetric(
                                  horizontal: 10, vertical: 5),
                              decoration: BoxDecoration(
                                  color: YhColors.reef,
                                  borderRadius:
                                      BorderRadius.circular(YhRadius.tag)),
                              child: Text(inviteRewardStatus(reward, now),
                                  style:
                                      TextStyle(color: color, fontSize: 12))),
                        ]),
                        const SizedBox(height: 14),
                        Text(inviteRewardHint(reward),
                            style: Theme.of(context)
                                .textTheme
                                .bodyMedium
                                ?.copyWith(color: YhColors.haze)),
                        const SizedBox(height: 12),
                        Text(
                            '${reward.isExample ? '示例记录' : '记录时间'} · ${accountDateLabel(reward.createdAt)}',
                            style: Theme.of(context).textTheme.labelSmall),
                      ]),
                )));
          },
        ),
      ),
    );
  }
}
