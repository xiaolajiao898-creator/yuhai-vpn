import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:uuid/uuid.dart';

import '../../../app/router.dart';
import '../../../app/theme/tokens.dart';
import '../../../core/network/api_client.dart';
import '../../account/data/account_api.dart';
import '../../settings/presentation/settings_widgets.dart';
import '../data/invite_api.dart';
import 'invite_clipboard.dart';

class InvitePage extends ConsumerStatefulWidget {
  const InvitePage({super.key});
  @override
  ConsumerState<InvitePage> createState() => _InvitePageState();
}

class _InvitePageState extends ConsumerState<InvitePage> {
  final _code = TextEditingController();
  bool _confirming = false,
      _submitting = false,
      _pasting = false,
      _bound = false;
  int _editVersion = 0;
  String _method = 'MANUAL';
  String? _error, _attemptPayload, _attemptKey, _successDescription;
  bool get _busy => _confirming || _submitting;

  @override
  void dispose() {
    _code.dispose();
    super.dispose();
  }

  Future<void> _copy(String text) async {
    try {
      await ref.read(inviteClipboardProvider).writeText(text);
      if (mounted) {
        ScaffoldMessenger.of(context)
            .showSnackBar(const SnackBar(content: Text('已复制')));
      }
    } catch (_) {
      if (mounted) {
        ScaffoldMessenger.of(context)
            .showSnackBar(const SnackBar(content: Text('复制失败，请手动选择复制')));
      }
    }
  }

  Future<void> _paste() async {
    if (_pasting || _busy) return;
    final version = _editVersion;
    setState(() {
      _pasting = true;
      _error = null;
    });
    try {
      // 只在用户明确点击“粘贴邀请码”后读取剪贴板。
      final text = await ref.read(inviteClipboardProvider).readText();
      if (!mounted || version != _editVersion || _busy || _bound) return;
      final code = text == null ? null : parseInviteCode(text);
      setState(() {
        if (code == null) {
          _error = '剪贴板中没有有效邀请码，请手动输入。';
        } else {
          _code.text = code;
          _method = 'CLIPBOARD';
        }
      });
    } catch (_) {
      if (mounted && version == _editVersion) {
        setState(() => _error = '无法读取剪贴板，请手动输入邀请码。');
      }
    } finally {
      if (mounted) setState(() => _pasting = false);
    }
  }

  Future<void> _refresh() async {
    if (_busy) return;
    ref.invalidate(inviteInfoProvider);
    try {
      await ref.read(inviteInfoProvider.future);
    } catch (_) {
      // Provider 的错误状态保留重试入口。
    }
  }

  Future<void> _submit() async {
    if (_busy || _bound) return;
    final current = ref.read(inviteInfoProvider).valueOrNull;
    if (current?.acceptsBinding != true) return;
    final code = parseInviteCode(_code.text);
    if (code == null) {
      setState(() => _error = '请输入 6 位有效邀请码，不包含 0、1、I、L、O。');
      return;
    }
    if (code == current!.code.toUpperCase()) {
      setState(() => _error = '不能绑定自己的邀请码。');
      return;
    }
    final method = _method;
    final rewardNotice = current.rewardsEnabled
        ? '奖励以活动规则和实际发放记录为准。'
        : '奖励活动暂未开放，当前仅绑定邀请关系，不发放奖励。';
    setState(() {
      _confirming = true;
      _error = null;
    });
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('确认绑定邀请码？'),
        content: Text('将绑定邀请码 $code。\n\n每个账户只能绑定一次，绑定后不能修改。\n\n$rewardNotice'),
        actions: [
          TextButton(
              onPressed: () => Navigator.pop(context, false),
              child: const Text('再检查一下')),
          TextButton(
              onPressed: () => Navigator.pop(context, true),
              child: const Text('确认绑定')),
        ],
      ),
    );
    if (!mounted) return;
    setState(() => _confirming = false);
    if (confirmed != true) return;
    if (ref.read(inviteInfoProvider).valueOrNull?.acceptsBinding != true) {
      setState(() => _error = '绑定状态已更新，请刷新后查看。');
      return;
    }
    final payload = '$code|$method';
    if (_attemptPayload != payload) {
      _attemptPayload = payload;
      _attemptKey = const Uuid().v4();
    }
    setState(() => _submitting = true);
    try {
      final result = await ref
          .read(inviteApiProvider)
          .bind(code: code, method: method, idempotencyKey: _attemptKey!);
      if (!mounted) return;
      if (!result.bound) throw StateError('Binding was not confirmed');
      setState(() {
        _bound = true;
        _successDescription = result.description;
      });
      ref.invalidate(inviteInfoProvider);
      // 成功后刷新；读取失败也保留已绑定提示，避免诱导重复提交。
      try {
        await ref.read(inviteInfoProvider.future);
      } catch (_) {}
    } on ApiException catch (error) {
      if (!mounted) return;
      setState(() => _error = switch (error.code) {
            40402 => '没有找到这个邀请码，请核对后重新输入。',
            40903 => '该账户已经绑定过邀请码，请刷新查看。',
            40904 => '当前账户已超过绑定期限或已完成付费，无法再绑定。',
            42201 => '不能绑定自己的邀请码。',
            40304 => '暂时无法绑定，请稍后查看。',
            40901 => '本次请求状态不一致，请刷新页面后重试。',
            42901 => '操作过于频繁，请稍后再试。',
            _ => '绑定结果暂未确认，请检查网络后重试。',
          });
      if (error.code == 40903 || error.code == 40904 || error.code == 40304) {
        ref.invalidate(inviteInfoProvider);
      }
    } catch (_) {
      if (mounted) setState(() => _error = '绑定结果暂未确认，请检查网络后重试。');
    } finally {
      if (mounted) setState(() => _submitting = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final info = ref.watch(inviteInfoProvider);
    return Scaffold(
      appBar: AppBar(title: const Text('邀请好友')),
      body: info.when(
        loading: () => Column(children: [
          if (_bound) _success(),
          const Expanded(child: Center(child: CircularProgressIndicator()))
        ]),
        error: (_, __) => Column(children: [
          if (_bound) _success(),
          Expanded(child: AccountLoadError(onRetry: _refresh))
        ]),
        data: (i) => RefreshIndicator(
          onRefresh: _refresh,
          child: ListView(
              physics: const AlwaysScrollableScrollPhysics(),
              padding: const EdgeInsets.all(YhSpace.page),
              children: [
                _activityStatus(i),
                const SizedBox(height: 20),
                SettingsPanel(
                    child: Padding(
                        padding: const EdgeInsets.all(YhSpace.panel),
                        child: Column(
                            crossAxisAlignment: CrossAxisAlignment.start,
                            children: [
                              Text('我的邀请码',
                                  style:
                                      Theme.of(context).textTheme.labelSmall),
                              const SizedBox(height: 12),
                              SelectableText(i.code,
                                  style: const TextStyle(
                                      fontSize: 36,
                                      fontWeight: FontWeight.w800,
                                      letterSpacing: 4)),
                              const SizedBox(height: 16),
                              FilledButton.icon(
                                  onPressed: () => _copy('宇海VPN邀请码 ${i.code}'),
                                  icon: const Icon(Icons.copy),
                                  label: const Text('复制邀请码')),
                              if (i.url != null)
                                TextButton.icon(
                                    onPressed: () => _copy(i.url!),
                                    icon: const Icon(Icons.link),
                                    label: const Text('复制邀请链接')),
                            ]))),
                const SizedBox(height: 20),
                LayoutBuilder(
                    builder: (context, constraints) =>
                        Wrap(spacing: 12, runSpacing: 12, children: [
                          _stat('邀请人数', i.stats['invited'],
                              (constraints.maxWidth - 12) / 2),
                          _stat('已付费人数', i.stats['paid'],
                              (constraints.maxWidth - 12) / 2),
                          _stat('待处理奖励（笔）', i.stats['rewards_pending'],
                              (constraints.maxWidth - 12) / 2),
                          _stat('已发放奖励（笔）', i.stats['rewards_released'],
                              (constraints.maxWidth - 12) / 2),
                        ])),
                const SizedBox(height: 24),
                _bindingCard(i),
                const SizedBox(height: 24),
                Text('活动规则', style: Theme.of(context).textTheme.titleLarge),
                const SizedBox(height: 12),
                Text(i.rules),
                const SizedBox(height: 12),
                Text('待处理奖励包含冻结中与审核中的记录，实际到账以已发放记录为准。',
                    style: Theme.of(context).textTheme.labelSmall),
                const SizedBox(height: 20),
                OutlinedButton(
                    onPressed: () => context.push(Routes.rewards),
                    child: const Text('查看奖励记录')),
              ]),
        ),
      ),
    );
  }

  Widget _bindingCard(InviteInfo info) {
    if (_bound) return _success();
    final available = info.acceptsBinding;
    final (title, detail) = switch (info.bindStatus) {
      'BOUND' => ('已绑定邀请码', '邀请关系已确认，每个账户只能绑定一次。'),
      'PAID' => ('已完成付费，无法补绑', '邀请码需要在首次付费前完成绑定。'),
      'EXPIRED' => ('绑定期限已结束', '该账户已超过邀请码补绑期限。'),
      'BLOCKED' => ('暂时无法绑定', '当前账户暂不符合绑定条件。'),
      'AVAILABLE' when available => ('绑定好友邀请码', '每个账户只能绑定一次，确认后不能修改。'),
      _ => ('邀请码绑定暂未开放', '开放后，可在有效期内绑定好友的邀请码。'),
    };
    return SettingsPanel(
        child: Padding(
            padding: const EdgeInsets.all(YhSpace.panel),
            child:
                Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
              Row(children: [
                const Icon(Icons.link, color: YhColors.starlight),
                const SizedBox(width: 8),
                Expanded(
                    child: Text(title,
                        style: Theme.of(context).textTheme.titleLarge))
              ]),
              const SizedBox(height: 8),
              Text(detail,
                  style: Theme.of(context)
                      .textTheme
                      .bodyMedium
                      ?.copyWith(color: YhColors.haze)),
              if (available) ...[
                if (info.bindDeadline != null)
                  Padding(
                      padding: const EdgeInsets.only(top: 8),
                      child: Text(
                          '绑定截止 · ${accountDateLabel(info.bindDeadline)}（本地时间）',
                          style: Theme.of(context).textTheme.labelSmall)),
                const SizedBox(height: 16),
                TextField(
                  key: const ValueKey('invite-bind-code'),
                  controller: _code,
                  enabled: !_busy,
                  maxLength: 32,
                  textCapitalization: TextCapitalization.characters,
                  autocorrect: false,
                  enableSuggestions: false,
                  decoration: const InputDecoration(
                      labelText: '好友邀请码',
                      hintText: '输入 6 位邀请码',
                      counterText: ''),
                  onChanged: (_) {
                    _editVersion++;
                    setState(() {
                      _method = 'MANUAL';
                      _error = null;
                    });
                  },
                ),
                Align(
                    alignment: Alignment.centerRight,
                    child: TextButton.icon(
                      onPressed: _pasting || _busy ? null : _paste,
                      icon: const Icon(Icons.content_paste, size: 18),
                      label: Text(_pasting ? '正在读取…' : '粘贴邀请码'),
                    )),
                if (_error != null)
                  Padding(
                      padding: const EdgeInsets.only(bottom: 12),
                      child: Text(_error!,
                          style: const TextStyle(color: YhColors.coral))),
                FilledButton(
                    onPressed: _busy ? null : _submit,
                    child: Text(_submitting ? '正在绑定…' : '绑定邀请码')),
              ] else if (_error != null)
                Padding(
                    padding: const EdgeInsets.only(top: 12),
                    child: Text(_error!,
                        style: const TextStyle(color: YhColors.coral))),
            ])));
  }

  Widget _activityStatus(InviteInfo info) => SettingsPanel(
        child: Padding(
          padding: const EdgeInsets.all(YhSpace.panel),
          child:
              Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            Row(children: [
              const Icon(Icons.link, color: YhColors.moonlight, size: 20),
              const SizedBox(width: 8),
              Expanded(
                  child: Text(info.bindingEnabled ? '邀请绑定已开放' : '邀请绑定暂未开放',
                      style: Theme.of(context).textTheme.titleMedium)),
            ]),
            const SizedBox(height: 12),
            Row(children: [
              const Icon(Icons.card_giftcard_outlined,
                  color: YhColors.starlight, size: 20),
              const SizedBox(width: 8),
              Expanded(
                  child: Text(info.rewardsEnabled ? '奖励以活动规则为准' : '奖励活动暂未开放',
                      style: Theme.of(context)
                          .textTheme
                          .titleMedium
                          ?.copyWith(color: YhColors.starlight))),
            ]),
            const SizedBox(height: 10),
            Text(
                info.rewardsEnabled
                    ? '绑定成功不代表已获得奖励，请以实际发放记录为准。'
                    : '当前仅绑定邀请关系，不发放奖励。历史奖励记录仍可查看。',
                style: Theme.of(context).textTheme.labelSmall),
          ]),
        ),
      );

  Widget _success() => Padding(
      padding: const EdgeInsets.symmetric(vertical: 8),
      child: SettingsPanel(
          child: Padding(
        padding: const EdgeInsets.all(YhSpace.panel),
        child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
          const Text('邀请码绑定成功',
              style: TextStyle(
                  fontSize: 20,
                  fontWeight: FontWeight.w700,
                  color: YhColors.starlight)),
          const SizedBox(height: 8),
          Text(_successDescription?.isNotEmpty == true
              ? _successDescription!
              : '邀请关系已确认，绑定后不能修改。'),
        ]),
      )));

  Widget _stat(String title, dynamic value, double width) => SizedBox(
      width: width,
      child: SettingsPanel(
          child: Padding(
              padding: const EdgeInsets.all(14),
              child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text('${value is int ? value : 0}',
                        style: const TextStyle(
                            fontSize: 26, fontWeight: FontWeight.w800)),
                    const SizedBox(height: 6),
                    Text(title),
                  ]))));
}
