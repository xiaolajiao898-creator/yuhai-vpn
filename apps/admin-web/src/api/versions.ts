import {adminApi, prepareProtectedRequest} from './client.ts';
import {readApproval, type Approval, type Page} from './review.ts';
import {draftProblem, normalizeDraft} from '../pages/versions-model.ts';

export type VersionPlatform = 'ANDROID' | 'IOS';
export type VersionStatus = 'DRAFT' | 'PUBLISHED' | 'WITHDRAWN';
export type VersionFields = {
  platform: VersionPlatform; version_name: string; version_code: string; min_supported_code: string;
  download_url: string; file_sha256: string; cert_sha256: string | null; release_notes: string;
};
export type AppVersion = Omit<VersionFields, 'download_url' | 'file_sha256'> & {
  id: string; download_url: string | null; file_sha256: string | null; status: VersionStatus;
  published_at: string | null; approval_id: string | null; created_at: string;
  snapshot_hash: string; pending_approval_id: string | null; can_edit: boolean;
};
export type VersionQuery = {page: number; page_size: number; platform?: VersionPlatform; status?: VersionStatus; version_code?: string};
export type SaveDraft = VersionFields & {operation_id: string; expected_snapshot_hash?: string};
export type VersionTarget = {id: string; snapshot_hash: string};
export type PreparedWrite<T> = {confirm(code: string): Promise<T>};
export type VersionSource = {
  list(query: VersionQuery): Promise<Page<AppVersion>>;
  get(id: string): Promise<AppVersion>;
  find(platform: VersionPlatform, code: string): Promise<AppVersion | null>;
  prepareSave(input: SaveDraft, target?: VersionTarget): Promise<PreparedWrite<AppVersion>>;
  preparePublish(version: AppVersion, reason: string): Promise<PreparedWrite<Approval>>;
};

// A failed verification cannot have sent a business write. Once sending begins,
// even a malformed success response requires a fresh read before any new action.
export class VersionWriteError extends Error {
  readonly uncertain: boolean;
  constructor(error: unknown, uncertain: boolean) {
    super(error instanceof Error ? error.message : '操作失败，请重新查询');
    this.name = 'VersionWriteError';
    this.uncertain = uncertain;
  }
}
const invalid = () => new Error('版本资料返回不完整，请重新查询确认结果');
const record = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v);
const id = (v: unknown): v is string => typeof v === 'string' && /^[1-9][0-9]{0,18}$/.test(v) && BigInt(v) <= 9223372036854775807n;
const nullableID = (v: unknown) => v === null || id(v);
const date = (v: unknown) => typeof v === 'string' && Number.isFinite(Date.parse(v));
const sha = (v: unknown) => typeof v === 'string' && /^[a-f0-9]{64}$/.test(v);
const code = (v: unknown, zero = false): v is string => typeof v === 'string' && (zero ? /^(0|[1-9][0-9]{0,9})$/ : /^[1-9][0-9]{0,9}$/).test(v) && BigInt(v) <= 2147483647n;
export function readVersion(v: unknown): AppVersion {
  if (!record(v) || !id(v.id) || typeof v.platform !== 'string' || !['ANDROID','IOS'].includes(v.platform) ||
      typeof v.version_name !== 'string' || !v.version_name || [...v.version_name].length > 32 ||
      !code(v.version_code) || !code(v.min_supported_code, true) || BigInt(v.min_supported_code) > BigInt(v.version_code) ||
      (v.download_url !== null && (typeof v.download_url !== 'string' || v.download_url.length > 512)) ||
      (v.file_sha256 !== null && !sha(v.file_sha256)) || (v.cert_sha256 !== null && !sha(v.cert_sha256)) ||
      typeof v.release_notes !== 'string' || new TextEncoder().encode(v.release_notes).length > 16000 ||
      typeof v.status !== 'string' || !['DRAFT','PUBLISHED','WITHDRAWN'].includes(v.status) || !date(v.created_at) || !sha(v.snapshot_hash) ||
      (v.published_at !== null && !date(v.published_at)) || !nullableID(v.approval_id) || !nullableID(v.pending_approval_id) ||
      typeof v.can_edit !== 'boolean' || v.can_edit !== (v.status === 'DRAFT' && v.pending_approval_id === null && v.published_at === null && v.approval_id === null) ||
      (v.status !== 'DRAFT' && v.pending_approval_id !== null)) throw invalid();
  return v as AppVersion;
}
export function sameVersionFields(version: AppVersion, input: VersionFields): boolean {
  return (['platform','version_name','version_code','min_supported_code','download_url','file_sha256','cert_sha256','release_notes'] as const).every(key => version[key] === input[key]);
}
function readVersionPage(v: unknown, q: VersionQuery): Page<AppVersion> {
  if (!record(v) || !Array.isArray(v.items) || v.items.length > q.page_size ||
      typeof v.total !== 'string' || !/^(0|[1-9][0-9]{0,18})$/.test(v.total) || BigInt(v.total) > 9223372036854775807n ||
      v.page !== q.page || v.page_size !== q.page_size) throw invalid();
  const items = v.items.map(readVersion), ids = new Set<string>();
  for (const item of items) {
    if (ids.has(item.id) || (q.platform && item.platform !== q.platform) || (q.status && item.status !== q.status) || (q.version_code && item.version_code !== q.version_code)) throw invalid();
    ids.add(item.id);
  }
  return {items, total: v.total, page: q.page, page_size: q.page_size};
}
async function protectedWrite<T>(method: 'POST' | 'PUT', path: string, body: unknown, action: string, read: (v: unknown) => T): Promise<PreparedWrite<T>> {
  const request = await prepareProtectedRequest<unknown>(method, path, body);
  let attempted = false, mayHaveSent = false;
  return Object.freeze({confirm: async (totp: string) => {
    if (attempted) throw new VersionWriteError(new Error('此确认已使用，请重新核对资料'), mayHaveSent);
    attempted = true;
    let sent = false;
    try {
      if (!/^\d{6}$/.test(totp)) throw new Error('请输入6位动态验证码');
      const token = await adminApi.stepUp({action_code: action, payload_hash: request.payloadHash, totp_code: totp});
      if (!token || !/^[A-Za-z0-9_-]{43}$/.test(token.step_up_token) || !date(token.expires_at)) throw invalid();
      sent = true; mayHaveSent = true;
      return read(await request.send(token.step_up_token));
    } catch (error) { throw new VersionWriteError(error, sent); }
  }});
}
export const liveVersionSource: VersionSource = {
  list: async q => {
    if (q.version_code && !q.platform) throw new Error('精确查询版本编号时，请同时选择平台');
    const query = new URLSearchParams(Object.entries(q).filter(([,v]) => v !== undefined && v !== '').map(([key,value]) => [key,String(value)]));
    return readVersionPage(await adminApi.get(`/app-versions?${query}`), q);
  },
  get: async target => {
    if (!id(target)) throw invalid();
    const value = readVersion(await adminApi.get(`/app-versions/${target}`));
    if (value.id !== target) throw invalid();
    return value;
  },
  find: async (platform, versionCode) => {
    const page = await liveVersionSource.list({page: 1, page_size: 10, platform, version_code: versionCode});
    if (page.total === '0' && page.items.length === 0) return null;
    if (page.total !== '1' || page.items.length !== 1) throw invalid();
    const value = await liveVersionSource.get(page.items[0].id);
    if (value.platform !== platform || value.version_code !== versionCode) throw invalid();
    return value;
  },
  prepareSave: async (input, target) => {
    const base = target ? Object.freeze({...target}) : undefined;
    const problem = draftProblem(input);
    if (problem) throw new Error(problem);
    if (!/^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$/.test(input.operation_id) ||
        (base && (!id(base.id) || !sha(base.snapshot_hash)))) throw invalid();
    const frozen = Object.freeze({...normalizeDraft(input), operation_id: input.operation_id, ...(base ? {expected_snapshot_hash: base.snapshot_hash} : {})});
    return protectedWrite(base ? 'PUT' : 'POST', base ? `/app-versions/${base.id}` : '/app-versions', frozen, 'app_versions.draft', raw => {
      const value = readVersion(raw);
      if ((base && value.id !== base.id) || value.status !== 'DRAFT' || value.published_at !== null || value.approval_id !== null || !sameVersionFields(value, frozen)) throw invalid();
      return value;
    });
  },
  preparePublish: async (version, reason) => {
    const frozen = readVersion(structuredClone(version));
    if (frozen.status !== 'DRAFT' || frozen.pending_approval_id || !frozen.can_edit || frozen.published_at !== null || frozen.approval_id !== null) throw new Error('此版本目前不能申请发布');
    if (!reason || reason !== reason.trim() || [...reason].length > 500) throw new Error('请填写1至500字的发布理由');
    const problem = draftProblem(normalizeDraft(frozen));
    if (problem) throw new Error(problem);
    return protectedWrite('POST', `/app-versions/${frozen.id}/publish`, {reason}, 'app_versions.publish', raw => {
      const value = readApproval(raw);
      if (value.action_code !== 'app_versions.publish' || value.target_type !== 'app_version' || value.target_id !== frozen.id ||
          value.status !== 'PENDING' || value.reason !== reason || value.payload_hash !== frozen.snapshot_hash || !value.executor_supported) throw invalid();
      return value;
    });
  },
};
