import type {AdminSession} from '../api/client.ts';
import type {AppVersion, VersionFields} from '../api/versions.ts';

export const versionStatusLabels = {DRAFT:'草稿', PUBLISHED:'已发布', WITHDRAWN:'已撤回'};
export const platformLabels = {ANDROID:'Android', IOS:'iOS'};
export const emptyDraft = (): VersionFields => ({platform:'ANDROID', version_name:'', version_code:'', min_supported_code:'0', download_url:'', file_sha256:'', cert_sha256:null, release_notes:''});
export function normalizeDraft(value: Partial<VersionFields> | AppVersion): VersionFields {
  return {platform:value.platform ?? 'ANDROID', version_name:value.version_name?.trim() ?? '', version_code:value.version_code?.trim() ?? '',
    min_supported_code:value.min_supported_code?.trim() ?? '', download_url:value.download_url?.trim() ?? '',
    file_sha256:value.file_sha256?.trim().toLowerCase() ?? '', cert_sha256:value.cert_sha256?.trim().toLowerCase() || null,
    release_notes:value.release_notes ?? ''};
}
export function publicDownloadURL(value: string): boolean {
  if (!value || value.length > 512 || value !== value.trim() || /[\\\s]/.test(value) || value.includes('?') || value.includes('#')) return false;
  try {
    const url = new URL(value);
    if (!value.startsWith('https://') || url.protocol !== 'https:' || url.username || url.password || url.port === '0') return false;
    const host = url.hostname.toLowerCase();
    if (!host.includes('.') || host.endsWith('.') || /\.(localhost|local|internal|lan|home|test|invalid)$/.test(host)) return false;
    // Only named HTTPS download locations, matching the server contract. Never
    // interpret numeric IPv4 aliases or IP literals as an approved CDN domain.
    if (/^[0-9.]+$/.test(host) || host.startsWith('[')) return false;
    const rawHost=value.slice(8).split('/')[0].replace(/:[0-9]+$/,'').toLowerCase();
    if (rawHost !== host) return false;
    return host.length <= 253 && host.split('.').every(label=>/^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(label)) && /[a-z]/.test(host.split('.').at(-1)!);
  } catch { return false; }
}
export function draftProblem(input: Partial<VersionFields> | AppVersion): string {
  const value = normalizeDraft(input);
  if (!['ANDROID','IOS'].includes(value.platform)) return '请选择 Android 或 iOS 平台';
  if (!value.version_name || [...value.version_name].length > 32 || /[\u0000-\u001f\u007f]/.test(value.version_name)) return '版本名称须为1至32个可见字符';
  if (!/^[1-9][0-9]{0,9}$/.test(value.version_code) || BigInt(value.version_code) > 2147483647n) return '版本编号须为1至2147483647的整数，不加逗号或前导零';
  if (!/^(0|[1-9][0-9]{0,9})$/.test(value.min_supported_code) || BigInt(value.min_supported_code) > BigInt(value.version_code)) return '最低支持编号须为0至当前版本编号之间的整数';
  if (!publicDownloadURL(value.download_url)) return '请填写公网 HTTPS 域名链接，不使用IP直链、账号密码、查询参数或片段';
  if (!/^[a-f0-9]{64}$/.test(value.file_sha256)) return '请填写安装包的64位 SHA256 校验值';
  if ((value.platform === 'ANDROID' && !value.cert_sha256) || (value.cert_sha256 !== null && !/^[a-f0-9]{64}$/.test(value.cert_sha256))) return 'Android须填写64位签名证书 SHA256；iOS可留空';
  if (new TextEncoder().encode(value.release_notes).length > 16000) return '更新说明过长，请缩短至16000字节以内';
  return '';
}
export function versionBlock(version: AppVersion, session: AdminSession, action: 'edit' | 'publish'): string {
  if (!session.permissions.includes(action === 'edit' ? 'app_versions.draft' : 'app_versions.publish')) return action === 'edit' ? '当前账号没有草稿编辑权限' : '当前账号没有发布申请权限';
  if (version.status !== 'DRAFT') return '已发布或已撤回的版本不能修改或再次申请';
  if (version.published_at !== null || version.approval_id !== null) return '版本状态需要核实，请重新查询';
  if (version.pending_approval_id) return '已有待审批申请，处理完成前不能修改';
  if (action === 'edit' && !version.can_edit) return '当前版本暂时不能编辑，请重新查询';
  return action === 'publish' ? draftProblem(version) : '';
}
