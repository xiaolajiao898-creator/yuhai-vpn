import type {AdminSession} from '../api/client';
import type {Approval} from '../api/review';
export const statusLabels: Record<Approval['status'], string> = {PENDING: '待审批', APPROVED: '已批准', REJECTED: '已拒绝', EXECUTED: '已执行', EXPIRED: '已过期', FAILED: '执行失败'};
export const actionLabel = (code: string) => ({'app_versions.publish': '版本发布', 'approvals.create': '发起审批', 'approvals.approve': '批准执行', 'approvals.reject': '拒绝申请', 'approvals.decide': '审批决定', 'invite.reward': '奖励规则', 'app_versions.draft':'保存版本草稿'}[code] ?? code);
export function decisionBlock(a: Approval, s: AdminSession, decision: 'approve' | 'reject', now = Date.now()): string {
  if (!s.permissions.includes('approvals.decide')) return '当前账号没有审批权限';
  if (a.requested_by === s.id) return '本人发起的申请须由另一位管理员审批';
  if (a.status !== 'PENDING') return '这张审批单已处理或失效';
  if (!Number.isFinite(Date.parse(a.expires_at)) || Date.parse(a.expires_at) <= now) return '审批已过期，请重新发起';
  if (decision === 'approve' && (!a.executor_supported || a.action_code !== 'app_versions.publish')) return '此类操作尚未接入执行功能';
  if (decision === 'approve' && !s.permissions.includes(a.action_code)) return '当前账号没有此项业务的执行权限';
  return '';
}
export function boundedTotal(total: string): number {
  if (!/^\d+$/.test(total)) return 0;
  return Number(BigInt(total) > BigInt(Number.MAX_SAFE_INTEGER) ? BigInt(Number.MAX_SAFE_INTEGER) : BigInt(total));
}
export function validRange(from: string, to: string): boolean {
  const pattern = /^[1-9][0-9]{0,18}$/;
  return pattern.test(from) && pattern.test(to) && BigInt(from) <= BigInt(to) && BigInt(to) <= 9223372036854775807n;
}

export const targetLabel = (value: string | null) => value ? ({app_version:'APP版本',approval:'审批单',invite_rule:'奖励规则'}[value] ?? value) : '系统';
