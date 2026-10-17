import { Alert, Button, Card, Form, Input, Steps } from 'antd';
import { useState } from 'react';
import { adminApi } from '../api/client';

export function LoginPage() {
  const [step, setStep] = useState(0);
  const [ticket, setTicket] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  async function login(v: {username: string; password: string}) {
    if (busy) return;
    setBusy(true); setError('');
    try { const r = await adminApi.login(v); setTicket(r.mfa_ticket); setStep(1); }
    catch (e) { setError(e instanceof Error ? e.message : '登录失败，请重试'); }
    finally { setBusy(false); }
  }
  async function verify(v: {totp_code: string}) {
    if (busy) return;
    setBusy(true); setError('');
    try { await adminApi.mfa({mfa_ticket: ticket, totp_code: v.totp_code}); location.replace('/dashboard'); }
    catch (e) { setError(e instanceof Error ? e.message : '验证失败，请重试'); }
    finally { setBusy(false); }
  }
  return (
    <div style={{ display: 'grid', placeItems: 'center', minHeight: '100vh', padding: 20 }}>
      <Card title="宇海加密VPN · 管理后台" style={{ width: '100%', maxWidth: 400 }}>
        <Steps size="small" current={step} items={[{ title: '账号密码' }, { title: '动态口令' }]} style={{ marginBottom: 24 }} />
        {error && <Alert type="error" showIcon message={error} style={{marginBottom: 16}} />}
        {step === 0 ? (
          <Form key="password" onFinish={login} autoComplete="on">
            <Form.Item name="username" rules={[{ required: true, message: '请输入用户名' }]}><Input placeholder="用户名" autoComplete="username" maxLength={32} /></Form.Item>
            <Form.Item name="password" rules={[{ required: true, message: '请输入密码' }]}><Input.Password placeholder="密码" autoComplete="current-password" maxLength={128} /></Form.Item>
            <Button type="primary" htmlType="submit" block loading={busy}>下一步</Button>
          </Form>
        ) : (
          <Form key="totp" onFinish={verify}>
            <Form.Item name="totp_code" rules={[{ required: true, pattern: /^\d{6}$/, message: '请输入 6 位动态口令' }]}><Input placeholder="6 位动态口令" maxLength={6} inputMode="numeric" autoComplete="one-time-code" /></Form.Item>
            <Button type="primary" htmlType="submit" block loading={busy}>登录</Button>
            <Button type="link" block disabled={busy} onClick={() => {setStep(0); setTicket(''); setError('');}}>重新输入账号密码</Button>
          </Form>
        )}
      </Card>
    </div>
  );
}
