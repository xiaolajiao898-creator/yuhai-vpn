import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom';
import { Alert, Button, Spin } from 'antd';
import { useEffect, useState, type ReactNode } from 'react';
import { adminApi, type AdminSession } from './api/client';
import { LoginPage } from './pages/Login';

import {liveReviewSource} from './api/review';
import {liveVersionSource} from './api/versions';
import {liveUserSource} from './api/users';
import {liveDeviceRevokeSource} from './api/device-revoke';
import {liveUserDeviceSource} from './api/user-devices';
import {liveNodeSource} from './api/nodes';
import {liveEgressSource} from './api/egress';
import {Workspace} from './Workspace';

export function App() {
  return <BrowserRouter><Routes><Route path="/login" element={<LoginPage/>}/><Route path="/*" element={<SessionGate>{session => <Workspace session={session} source={liveReviewSource} versions={liveVersionSource} users={liveUserSource} devices={liveUserDeviceSource} revocations={liveDeviceRevokeSource} nodes={liveNodeSource} egress={liveEgressSource} onLogout={async()=>{try{await adminApi.logout();location.replace('/login');}catch{window.alert('退出失败，请重试');}}}/>}</SessionGate>}/></Routes></BrowserRouter>;
}

function SessionGate({children}: {children: (session: AdminSession) => ReactNode}) {
  const [session, setSession] = useState<AdminSession | null>();
  const [error, setError] = useState('');
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    let active = true;
    setError(''); setSession(undefined);
    adminApi.me().then(s => {if (active) setSession(s);}).catch(e => {
      if (!active) return;
      if (e.status === 401) setSession(null);
      else setError('暂时无法连接后台，请重试');
    });
    return () => {active = false;};
  }, [attempt]);
  if (error) return <div style={{padding: 32}}><Alert type="error" message={error} /><Button onClick={() => setAttempt(a => a+1)}>重试</Button></div>;
  if (session === undefined) return <div style={{padding: 48}}><Spin tip="正在验证登录状态" fullscreen /></div>;
  if (session === null) return <Navigate to="/login" replace />;
  return <>{children(session)}</>;
}
