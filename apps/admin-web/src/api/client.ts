// Cookie sessions + CSRF. Step-up tokens are passed explicitly, never cached globally.
export type AdminSession = {id: string; username: string; permissions: string[]; csrf_token: string};
export type StepUpResult = {step_up_token: string; expires_at: string};
let csrfToken = '';
let sessionEpoch = 0;
function setSession(csrf: string) {
  if (csrfToken !== csrf) sessionEpoch++;
  csrfToken = csrf;
}

function target(path: string): string {
  const raw = `/admin/v1${path}`;
  const parsed = new URL(raw, 'https://admin.invalid');
  if (!path.startsWith('/') || path.includes('\\') || parsed.hash || parsed.origin !== 'https://admin.invalid' || `${parsed.pathname}${parsed.search}` !== raw) {
    throw new Error('管理请求路径无效');
  }
  return raw;
}

async function request<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  return send<T>(method, target(path), body === undefined ? undefined : JSON.stringify(body), undefined, signal);
}
async function send<T>(method: string, uri: string, rawBody?: string, stepUpToken?: string, signal?: AbortSignal): Promise<T> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  if (method !== 'GET' && csrfToken) headers['X-CSRF-Token'] = csrfToken;
  if (stepUpToken) headers['X-Step-Up-Token'] = stepUpToken;
  const res = await fetch(uri, { method, headers, body: rawBody, credentials: 'same-origin', redirect: 'error', cache: 'no-store', referrerPolicy: 'no-referrer', ...(signal ? {signal} : {}) });
  const json = await res.json().catch(() => ({}));
  if (!res.ok) {
    throw Object.assign(new Error(json?.error?.message ?? res.statusText), { status: res.status, code: json?.error?.code, requestId: json?.request_id });
  }
  return json as T;
}

// Prepare once before displaying confirmation. The exact bytes being confirmed
// are hashed and later sent, even if a form object changes while TOTP is entered.
export async function prepareProtectedRequest<T>(method: 'POST' | 'PATCH' | 'PUT' | 'DELETE', path: string, body?: unknown) {
  const epoch = sessionEpoch;
  const uri = target(path);
  const rawBody = body === undefined ? undefined : JSON.stringify(body);
  const bytes = new TextEncoder().encode(`v1\n${method}\n${uri}\n${rawBody ?? ''}`);
  const digest = await crypto.subtle.digest('SHA-256', bytes);
  const payloadHash = Array.from(new Uint8Array(digest), b => b.toString(16).padStart(2, '0')).join('');
  let attempted = false;
  return Object.freeze({
    payloadHash,
    send: async (token: string): Promise<T> => {
      if (epoch !== sessionEpoch) throw new Error('登录状态已变化，请重新打开操作');
      if (!/^[A-Za-z0-9_-]{43}$/.test(token) || attempted) throw new Error('请重新核实操作结果');
      attempted = true; // A different token must not replay an uncertain write.
      return send<T>(method, uri, rawBody, token);
    },
  });
}

export const adminApi = {
  me: async () => {
    const epoch = sessionEpoch;
    const r = await request<AdminSession>('GET', '/auth/me');
    // 并发的两次 me() 会互相把 epoch 推进一格；只要拿到的仍是当前这份会话（csrf 相同）就不算变化。
    // 真正的登出/换号会让 csrfToken 变成空串或别的值，照样拒绝。
    if (epoch !== sessionEpoch && csrfToken !== r.csrf_token) throw new Error('登录状态已变化');
    setSession(r.csrf_token); return r;
  },
  logout: async () => {
    const pending = request<void>('POST', '/auth/logout');
    sessionEpoch++; csrfToken = '';
    await pending;
  },
  login: (v: { username: string; password: string }) => {
    sessionEpoch++; csrfToken = '';
    return request<{ mfa_ticket: string }>('POST', '/auth/login', v);
  },
  mfa: async (v: { mfa_ticket: string; totp_code: string }) => {
    const epoch = sessionEpoch;
    const r = await request<{ csrf_token: string }>('POST', '/auth/mfa', v);
    if (epoch !== sessionEpoch) throw new Error('登录状态已变化');
    setSession(r.csrf_token);
    return r;
  },
  stepUp: (v: { action_code: string; payload_hash: string; totp_code: string }) => request<StepUpResult>('POST', '/step-up', v),
  get: <T>(path: string, signal?: AbortSignal) => request<T>('GET', path, undefined, signal),
  post: <T>(path: string, body: unknown) => request<T>('POST', path, body),
  patch: <T>(path: string, body: unknown) => request<T>('PATCH', path, body),
};
