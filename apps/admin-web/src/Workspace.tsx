import {useState} from 'react';
import {Link, Navigate, Route, Routes, useLocation} from 'react-router-dom';
import {Button, Drawer, Tag} from 'antd';
import {AppstoreOutlined, AuditOutlined, CheckSquareOutlined, MenuOutlined, SafetyCertificateOutlined, FileProtectOutlined, TeamOutlined, CloudServerOutlined, GlobalOutlined} from '@ant-design/icons';
import type {AdminSession} from './api/client';
import type {ReviewSource} from './api/review';
import type {VersionSource} from './api/versions';
import type {UserSource} from './api/users';
import type {DeviceRevokeSource} from './api/device-revoke';
import type {UserDeviceSource} from './api/user-devices';
import {ApprovalsPage, AuditPage} from './pages/Reviews';
import {VersionsPage} from './pages/Versions';
import {UsersPage} from './pages/Users';
import type {NodeSource} from './api/nodes';
import {NodesPage} from './pages/Nodes';
import {ConfigEditor} from './pages/ConfigEditor';
import {OperationsPage,DashboardPage} from './pages/Operations';
import {EgressPage} from './pages/Egress';
import type {EgressSource} from './api/egress';
import './workspace.css';
const existingPages = [['admins','管理员'],['rewards','邀请奖励'],['exports','数据导出'],['scan-cursors','扫描游标'],['devices','设备'],['plans','套餐'],['orders','订单'],['chain-transfers','链上记录'],['deposit-exceptions','异常到账'],['receive-addresses','收款地址'],['traffic','流量'],['invites','邀请'],['blacklist','黑名单'],['configs','系统参数']];
// 按运营任务分组（第 21 轮信息架构）：不再把大半模块藏进「其他模块」。
const navGroups: [string, [string,string][]][] = [
  ['总览', [['/dashboard','运营总览']]],
  ['用户与设备', [['/users','用户管理'],['/devices','设备']]],
  ['线路与质量', [['/nodes','自建节点'],['/egress','出口线路'],['/traffic','流量']]],
  ['套餐与订单', [['/plans','套餐'],['/orders','订单'],['/deposit-exceptions','异常到账'],['/chain-transfers','链上记录'],['/receive-addresses','收款地址'],['/scan-cursors','扫描游标']]],
  ['推广与奖励', [['/invites','邀请'],['/rewards','邀请奖励'],['/blacklist','黑名单']]],
  ['系统与审计', [['/approvals','审批中心'],['/audit-logs','操作日志'],['/app-versions','版本管理'],['/admins','管理员'],['/exports','数据导出'],['/configs','系统参数']]],
];
const mainPages: Record<string,string> = {'/dashboard':'运营总览','/nodes':'自建节点','/egress':'出口线路','/users':'用户管理','/app-versions':'版本管理','/approvals':'审批中心','/audit-logs':'操作日志'};
// 面包屑覆盖全部路由；此前只认 5 个主页面，其余一律错显示为「审批中心」。
function breadcrumbLabel(path:string):string{
  if(mainPages[path])return mainPages[path];
  const key=path.replace(/^\//,'').split('/')[0];
  const found=existingPages.find(([k])=>k===key);
  return found?found[1]:'审批中心';
}
export function Workspace({session,source,versions,users,devices,revocations,nodes,egress,demo=false,onLogout}: {session:AdminSession;source:ReviewSource;versions:VersionSource;users:UserSource;devices:UserDeviceSource;revocations:DeviceRevokeSource;nodes:NodeSource;egress:EgressSource;demo?:boolean;onLogout?:()=>void}) {
  const path=useLocation().pathname;const [menu,setMenu]=useState(false);
  const icons:Record<string,JSX.Element>={'/dashboard':<AppstoreOutlined/>,'/users':<TeamOutlined/>,'/nodes':<CloudServerOutlined/>,'/egress':<GlobalOutlined/>,'/approvals':<CheckSquareOutlined/>,'/audit-logs':<AuditOutlined/>,'/app-versions':<FileProtectOutlined/>};
  const links=<>{navGroups.map(([group,items])=><div key={group}><div className="nav-group">{group}</div>{items.map(([to,label])=><Link key={to} to={to} className={`nav-link ${icons[to]?'':'sub'} ${path===to?'current':''}`} aria-current={path===to?'page':undefined} onClick={()=>setMenu(false)}>{icons[to]??null}{label}</Link>)}</div>)}</>;
  return <div className="admin-shell"><aside className="sidebar"><div className="brand"><svg className="brand-mark" viewBox="0 0 64 64" aria-hidden="true"><rect width="64" height="64" rx="16" fill="#bbef68"/><g fill="none" stroke="#14201c" strokeWidth="4" strokeLinecap="round"><path d="M13 22q9-8 19 0t19 0M13 32q9-8 19 0t19 0M13 42q9-8 19 0t19 0"/></g></svg><div>宇海<span>运营后台</span></div></div>{links}<div className="sidebar-bottom"><SafetyCertificateOutlined/><span>每次变更<br/><strong>有据可查</strong></span></div></aside><div className="main-shell"><header className="workspace-header"><div><Button className="mobile-menu" type="text" icon={<MenuOutlined/>} aria-label="打开导航" onClick={()=>setMenu(true)}/><span className="breadcrumb">工作台 / {breadcrumbLabel(path)}</span></div><div className="account-chip"><span className="avatar">{session.username.slice(0,1).toUpperCase()}</span><span className="account-name">{session.username}</span><Tag color={demo?'gold':'green'}>{demo?'演示账号':'已验证会话'}</Tag>{onLogout&&<Button type="text" onClick={onLogout}>退出</Button>}</div></header>{demo&&<div className="demo-ribbon"><strong>本机演示</strong><span>数据仅在内存中变化，刷新恢复；不会调用真实后台或发布安装包。</span></div>}<main className="workspace-content"><Routes><Route path="/dashboard" element={<DashboardPage session={session} demo={demo}/>}/><Route path="/nodes" element={<NodesPage source={nodes} session={session}/>}/><Route path="/egress" element={<EgressPage source={egress} session={session}/>}/><Route path="/users" element={<UsersPage source={users} devices={devices} revocations={revocations} session={session} demo={demo}/>}/><Route path="/app-versions" element={<VersionsPage source={versions} reviews={source} session={session} demo={demo}/>}/><Route path="/approvals" element={<ApprovalsPage source={source} session={session} demo={demo}/>}/><Route path="/audit-logs" element={<AuditPage source={source} session={session} demo={demo}/>}/>{existingPages.map(([key,label])=><Route key={key} path={`/${key}`} element={key==='configs'?<ConfigEditor session={session} demo={demo}/>:<OperationsPage key={key} moduleKey={key} session={session} demo={demo}/>}/>)}<Route path="*" element={<Navigate to="/approvals" replace/>}/></Routes></main></div><Drawer placement="left" open={menu} onClose={()=>setMenu(false)} title="宇海 · 管理工作台" width={260}><nav className="mobile-nav">{links}</nav></Drawer></div>;
}
