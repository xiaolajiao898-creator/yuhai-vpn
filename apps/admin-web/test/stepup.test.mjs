import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { adminApi, prepareProtectedRequest } from '../src/api/client.ts';

const token = 'a'.repeat(43);
test('hash binds exact target and immutable UTF-8 body, token is not cached', async t => {
  const calls = [];
  const original = globalThis.fetch;
  t.after(() => { globalThis.fetch = original; });
  globalThis.fetch = async (url, options) => { calls.push({url, ...options}); return new Response('{}'); };
  const input = { status: 'ACTIVE', reason: '用户确认' };
  const request = await prepareProtectedRequest('POST', '/users/17/status', input);
  const raw = JSON.stringify(input);
  const expected = createHash('sha256').update(`v1\nPOST\n/admin/v1/users/17/status\n${raw}`).digest('hex');
  assert.equal(request.payloadHash, expected);
  input.status = 'BANNED';
  await request.send(token);
  assert.equal(calls[0].body, raw);
  assert.equal(calls[0].headers['X-Step-Up-Token'], token);
  assert.equal(calls[0].redirect, 'error');
  assert.equal(calls[0].cache, 'no-store');
  assert.equal(calls[0].credentials, 'same-origin');
  await assert.rejects(request.send(token));
  assert.equal(calls.length, 1);
  await adminApi.post('/normal-write', { value: 0 });
  assert.equal(calls[1].headers['X-Step-Up-Token'], undefined);
});
test('uncertain writes are not silently retried with the same token', async t => {
  const original = globalThis.fetch;
  t.after(() => { globalThis.fetch = original; });
  let calls = 0;
  globalThis.fetch = async () => { calls++; throw new Error('network unavailable'); };
  const request = await prepareProtectedRequest('DELETE', '/devices/27');
  await assert.rejects(request.send(token));
  await assert.rejects(request.send(token));
  await assert.rejects(request.send('b'.repeat(43)));
  assert.equal(calls, 1);
});
test('logout or a new login invalidates already prepared operations', async t => {
  const original = globalThis.fetch;
  t.after(() => { globalThis.fetch = original; });
  const calls = [];
  globalThis.fetch = async (url, options) => { calls.push({url, ...options}); return new Response('{}'); };
  const beforeLogout = await prepareProtectedRequest('DELETE', '/devices/27');
  await adminApi.logout();
  await assert.rejects(beforeLogout.send(token), /登录状态已变化/);
  const beforeLogin = await prepareProtectedRequest('DELETE', '/devices/28');
  await adminApi.login({username: 'test', password: 'test-only'});
  await assert.rejects(beforeLogin.send(token), /登录状态已变化/);
  assert.deepEqual(calls.map(c => c.url), ['/admin/v1/auth/logout', '/admin/v1/auth/login']);
});
test('a late session read cannot restore CSRF after logout', async t => {
  const original = globalThis.fetch;
  t.after(() => { globalThis.fetch = original; });
  let resolveMe;
  const calls = [];
  globalThis.fetch = (url, options) => {
    calls.push({url, ...options});
    if (url.endsWith('/auth/me')) return new Promise(resolve => { resolveMe = resolve; });
    return Promise.resolve(new Response('{}'));
  };
  const pendingMe = adminApi.me();
  await adminApi.logout();
  resolveMe(new Response(JSON.stringify({csrf_token: 'STALE_CSRF_CANARY'})));
  await assert.rejects(pendingMe, /登录状态已变化/);
  await adminApi.post('/normal-write', {});
  assert.equal(calls.at(-1).headers['X-CSRF-Token'], undefined);
});
test('different targets, methods and bodies cannot share a hash', async () => {
  const values = await Promise.all([
    prepareProtectedRequest('DELETE', '/devices/1'),
    prepareProtectedRequest('DELETE', '/devices/2'),
    prepareProtectedRequest('POST', '/devices/1'),
    prepareProtectedRequest('DELETE', '/devices/1', {}),
    prepareProtectedRequest('DELETE', '/devices/1?force=1'),
  ]);
  assert.equal(new Set(values.map(v => v.payloadHash)).size, values.length);
  for (const path of ['/devices/../users/1', '/devices/1#other', '/devices\\x', '/%2e%2e/users/1']) {
    await assert.rejects(prepareProtectedRequest('DELETE', path));
  }
});
