import {adminApi, prepareProtectedRequest} from './client.ts';

export type ApprovalStatus = 'PENDING' | 'APPROVED' | 'REJECTED' | 'EXECUTED' | 'EXPIRED' | 'FAILED';
export type Approval = {
  id: string; action_code: string; target_type: string; target_id: string | null;
  status: ApprovalStatus; reason: string; requested_by: string; decided_by?: string | null;
  created_at: string; expires_at: string; decided_at?: string | null; executed_at?: string | null;
  decision_note?: string | null; payload_hash: string; executor_supported: boolean;
  snapshot?: Record<string, string | null>;
};
export type AuditEntry = {
  id: string; admin_user_id: string | null; action_code: string; target_type: string | null; target_id: string | null;
  approval_id: string | null; request_id: string; result: 'SUCCESS' | 'FAILED' | 'DENIED';
  step_up_verified: boolean; created_at: string; prev_hash: string; row_hash: string;
};
export type Page<T> = {items: T[]; total: string; page: number; page_size: number};
export type Verification = {
  valid: boolean; from_id: string; to_id: string; checked: number; head_id: string; head_hash: string;
  first_broken_id?: string | null; reason: string; external_anchor: false;
};
export type ApprovalQuery = {page: number; page_size: number; scope: 'all' | 'mine' | 'pending'; status?: string};
export type AuditQuery = {page: number; page_size: number; action?: string; result?: string};
export type ReviewSource = {
  listApprovals(query: ApprovalQuery): Promise<Page<Approval>>;
  getApproval(id: string): Promise<Approval>;
  prepareDecision(id: string, decision: 'approve' | 'reject', note: string): Promise<{confirm(code: string): Promise<Approval>}>;
  listAudit(query: AuditQuery): Promise<Page<AuditEntry>>;
  verify(from: string, to: string): Promise<Verification>;
};
const invalid = () => new Error('后台返回的数据不完整，请重新查询确认操作结果');
const record = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v);
const identifier = (v: unknown): v is string => typeof v === 'string' && /^[1-9][0-9]{0,18}$/.test(v) && BigInt(v) <= 9223372036854775807n;
const nullableID = (v: unknown) => v === null || identifier(v);
const date = (v: unknown) => typeof v === 'string' && Number.isFinite(Date.parse(v));
const digest = (v: unknown) => typeof v === 'string' && /^[a-f0-9]{64}$/.test(v);
const count = (v: unknown) => typeof v === 'string' && /^(0|[1-9][0-9]{0,18})$/.test(v) && BigInt(v) <= 9223372036854775807n;
export function readApproval(v: unknown): Approval {
  if (!record(v) || !identifier(v.id) || !identifier(v.requested_by) || !nullableID(v.target_id) ||
      !['PENDING','APPROVED','REJECTED','EXECUTED','EXPIRED','FAILED'].includes(String(v.status)) ||
      !['action_code','target_type','reason'].every(k=>typeof v[k] === 'string') ||
      !date(v.created_at) || !date(v.expires_at) || !digest(v.payload_hash) || typeof v.executor_supported !== 'boolean' ||
      (v.decided_by != null && !identifier(v.decided_by)) ||
      (v.decision_note != null && typeof v.decision_note !== 'string') ||
      (v.decided_at != null && !date(v.decided_at)) || (v.executed_at != null && !date(v.executed_at)) ||
      (v.snapshot !== undefined && (!record(v.snapshot) || Object.values(v.snapshot).some(x=>x !== null && typeof x !== 'string')))) throw invalid();
  return v as Approval;
}
function readAudit(v: unknown): AuditEntry {
  if (!record(v) || !identifier(v.id) || !nullableID(v.admin_user_id) || !nullableID(v.target_id) || !nullableID(v.approval_id) ||
      !['action_code','request_id'].every(k=>typeof v[k] === 'string') || (v.target_type !== null && typeof v.target_type !== 'string') || !date(v.created_at) ||
      !['SUCCESS','FAILED','DENIED'].includes(String(v.result)) || typeof v.step_up_verified !== 'boolean' || !digest(v.prev_hash) || !digest(v.row_hash)) throw invalid();
  return v as AuditEntry;
}
function readPage<T>(v: unknown, q: {page:number;page_size:number}, reader:(v:unknown)=>T):Page<T> {
  if (!record(v) || !Array.isArray(v.items) || !count(v.total) || v.page !== q.page || v.page_size !== q.page_size || v.items.length > q.page_size) throw invalid();
  return {items:v.items.map(reader),total:v.total as string,page:q.page,page_size:q.page_size};
}
const queryString = (q: object) => new URLSearchParams(Object.entries(q).filter(([,v]) => v !== '' && v !== undefined).map(([k,v]) => [k,String(v)])).toString();
export const liveReviewSource: ReviewSource = {
  listApprovals: async q => readPage(await adminApi.get(`/approvals?${queryString(q)}`), q, readApproval),
  getApproval: async id => {
    const a=readApproval(await adminApi.get(`/approvals/${encodeURIComponent(id)}`));
    if(a.id!==id) throw invalid();
    return a;
  },
  prepareDecision: async (id, decision, note) => {
    const prepared = await prepareProtectedRequest<unknown>('POST', `/approvals/${encodeURIComponent(id)}/${decision}`, decision === 'reject' ? {note} : {});
    let attempted = false;
    return {confirm: async code => {
      if (attempted) throw new Error('请先重新查询审批详情');
      attempted = true; // Uncertain writes never retry, even with a new token.
      const token = await adminApi.stepUp({action_code: 'approvals.decide', payload_hash: prepared.payloadHash, totp_code: code});
      const a = readApproval(await prepared.send(token.step_up_token));
      if (a.id !== id || a.status !== (decision === 'approve' ? 'EXECUTED' : 'REJECTED') || !a.decided_by || !a.decided_at ||
          (decision === 'approve' && !a.executed_at) || (decision === 'reject' && a.decision_note !== note)) throw invalid();
      return a;
    }};
  },
  listAudit: async q => readPage(await adminApi.get(`/audit-logs?${queryString(q)}`),q,readAudit),
  verify: async (from, to) => {
    const v=await adminApi.post<unknown>('/audit-logs/verify', {from_id: from, to_id: to});
    if(!record(v) || typeof v.valid!=='boolean' || v.from_id!==from || v.to_id!==to || !count(v.head_id) || !digest(v.head_hash) ||
       !Number.isInteger(v.checked) || Number(v.checked)<0 || Number(v.checked)>10000 || typeof v.reason!=='string' ||
       v.external_anchor!==false || (v.first_broken_id!=null && !identifier(v.first_broken_id))) throw invalid();
    return v as Verification;
  },
};
