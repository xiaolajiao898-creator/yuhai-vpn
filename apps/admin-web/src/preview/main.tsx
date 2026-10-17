import React from 'react';
import ReactDOM from 'react-dom/client';
import {HashRouter} from 'react-router-dom';
import {ConfigProvider} from 'antd';
import zhCN from 'antd/locale/zh_CN';
import {Workspace} from '../Workspace';
import {makePreviewSource} from './source';
import {makePreviewUsers} from './users';
import {makePreviewUserDevices} from './user-devices';
import {makePreviewNodes} from './nodes';
import {makePreviewEgress} from './egress';
const session={id:'18',username:'演示管理员',permissions:['nodes.read','egress.read','egress.write','users.read','users.ledgers.read','devices.revoke','approvals.read','approvals.decide','app_versions.read','app_versions.draft','app_versions.publish','audit.read'],csrf_token:''};
// Separate entry: there is no authentication bypass in production App.tsx.
// Enforce offline-only behavior even if a future component accidentally fetches.
window.fetch=async()=>{throw new Error('演示页面禁止请求真实接口');};
const source=makePreviewSource(session);
const users=makePreviewUsers(session);
const nodes=makePreviewNodes(session);
const egress=makePreviewEgress(session);
const devices=makePreviewUserDevices(session,users,source.recordDeviceRevocation);
ReactDOM.createRoot(document.getElementById('root')!).render(<React.StrictMode><ConfigProvider locale={zhCN} button={{autoInsertSpace:false}} theme={{token:{colorPrimary:'#157fa6',colorText:'#112c43',colorBorder:'#dce5ed',borderRadius:10,fontFamily:"-apple-system, BlinkMacSystemFont, 'PingFang SC', 'Noto Sans SC', 'Segoe UI', sans-serif"}}}><HashRouter><div className="preview-controls"><a href="http://127.0.0.1:15174/#/home">查看手机前端</a><span>演示验证码：123456</span><button onClick={()=>location.reload()}>重置示例</button></div><Workspace source={source} versions={source.versions} users={users} devices={devices} revocations={devices.revocations} nodes={nodes} egress={egress} session={session} demo/></HashRouter></ConfigProvider></React.StrictMode>);
