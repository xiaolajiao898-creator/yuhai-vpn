import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../account/data/account_api.dart';
import '../../traffic/data/traffic_api.dart';
import '../data/invite_api.dart';

final inviteNowProvider = Provider<DateTime Function()>((_) => DateTime.now);

String inviteRewardValue(InviteReward reward) => switch (reward.kind) {
      'DAYS' when reward.days != null && reward.days! >= 0 =>
        '${reward.days} 天',
      'TRAFFIC' when reward.bytes != null && reward.bytes! >= 0 =>
        formatTrafficBytes(reward.bytes!),
      'BALANCE' when reward.amount != null =>
        '${accountBalanceLabel(reward.amount)} USDT',
      _ => '奖励信息待确认',
    };

String inviteRewardStatus(InviteReward reward, DateTime now) =>
    switch (reward.status) {
      'PENDING' => reward.releaseAt?.isAfter(now) == true ? '冻结中' : '待发放',
      'REVIEW' => '审核中',
      'RELEASED' => '已发放',
      'REJECTED' => '已取消',
      _ => '处理中',
    };

String inviteRewardHint(InviteReward reward) => switch (reward.status) {
      'PENDING' => reward.releaseAt == null
          ? '冻结期结束后进行发放核验，暂未计入可用权益。'
          : '冻结结束 · ${accountDateLabel(reward.releaseAt)}\n到期后仍需通过发放核验。',
      'REVIEW' => '正在核验，暂未计入可用权益。',
      'RELEASED' => '已计入账户权益，可在账户流水查看。',
      'REJECTED' => '此笔奖励已取消，未计入账户权益。',
      _ => '处理结果请以最终记录为准。',
    };
