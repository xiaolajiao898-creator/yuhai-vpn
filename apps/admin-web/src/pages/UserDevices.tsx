import {useEffect,useLayoutEffect,useMemo,useRef,useState} from 'react';
import {Alert,Button,Empty,Input,Modal,Select,Table,Tag} from 'antd';
import {AppleOutlined,AndroidOutlined,ReloadOutlined} from '@ant-design/icons';
import {loadUserDevices,type UserDevice,type UserDevicePage,type UserDeviceQuery,type UserDeviceSource} from '../api/user-devices';
import {createLatestRead,stateForRead,type ReadState} from './latest-read';
import {deviceName,deviceRevoked,deviceSeen,deviceStatusLabels} from './user-devices-model';
import {userPageTotal,userReadError,userTime} from './users-model';
import type {AdminSession} from '../api/client';
import type {DeviceRevokeSource} from '../api/device-revoke';
import {canRevokeDevice,createDeviceRevokeFlow,type DeviceRevokeState} from './device-revoke-model';
import './user-devices.css';

type DeviceProps={source:UserDeviceSource;revocations:DeviceRevokeSource;session:AdminSession;userId:string;accountNo:string;active:boolean;scope:string;demo?:boolean;onChanged:(message:string)=>void};
export function UserDevices(props:DeviceProps) {
  return <DeviceRecords key={props.userId} {...props}/>;
}
function DeviceRecords({source,revocations,session,userId,accountNo,active,scope,demo=false,onChanged}:DeviceProps) {
  const [action,setAction]=useState<DeviceRevokeState>(),[reason,setReason]=useState(''),[otp,setOtp]=useState('');
  const actionScope=active?`${scope}:${session.id}:${[...session.permissions].sort().join(',')}`:undefined;
  const live=useRef({actionScope,onChanged});live.current={actionScope,onChanged};
  const flow=useMemo(()=>createDeviceRevokeFlow(revocations,setAction,(target,result)=>{
    if(live.current.actionScope!==actionScope||target.userId!==userId)return;
    const label=`${demo?'示例账户':'账户'} ${target.accountNo} 的设备 #${target.device.id}`;
    live.current.onChanged(result?`${label}${result.already_revoked?'原已撤销':'已撤销'}；${result.node_sync==='QUEUED'?'节点同步已排队':'无需新增节点同步'}。实际隧道断开以节点执行结果为准。`:`已查询到${label}当前为已撤销；这仅确认当前状态，不证明由刚才的请求完成。`);
  }),[revocations,actionScope,userId,demo]);
  useLayoutEffect(()=>{flow.context(actionScope);return()=>flow.cancel();},[flow,actionScope]);
  const shown=action?.scope===actionScope?action:undefined;
  useEffect(()=>{setOtp('');if(shown?.phase==='form')setReason(shown.reason);},[shown?.phase,shown?.target.device.id]);
  const hasRevoke=session.permissions.includes('users.read')&&session.permissions.includes('devices.revoke');
  const [query,setQuery]=useState<UserDeviceQuery>({page:1,page_size:20});
  const [refresh,setRefresh]=useState(0),[state,setState]=useState<ReadState<UserDevicePage>>();
  const channel=useMemo(()=>createLatestRead<UserDevicePage>(setState),[]);
  const key=active?`${scope}:devices:${userId}:${JSON.stringify(query)}:${refresh}`:undefined;
  useEffect(()=>{
    if(key===undefined){channel.cancel();return;}
    void channel.start(key,signal=>loadUserDevices(source,userId,query,signal));
    return channel.cancel;
  },[key,channel,source,userId,query]);
  const current=stateForRead(state,key),value=current?.phase==='ready'?current.value:undefined;
  return <section className="user-devices user-ledger" aria-label="账户设备记录">
    <div className="device-list-toolbar"><div><strong>设备绑定记录</strong><span>共 <b className="numeric">{value?.total??'—'}</b> 条</span></div><div><Select aria-label="设备状态筛选" value={query.status??'ALL'} options={[{value:'ALL',label:'全部记录'},{value:'ACTIVE',label:'已绑定'},{value:'REVOKED',label:'已撤销'}]} onChange={status=>setQuery({...query,status:status==='ACTIVE'||status==='REVOKED'?status:undefined,page:1})}/><Button aria-label="刷新设备记录" icon={<ReloadOutlined/>} loading={current?.phase==='loading'} onClick={()=>setRefresh(v=>v+1)}>刷新</Button></div></div>
    <p>已绑定表示设备尚未撤销。最后活跃时间是最近一次记录，不代表此刻在线。</p>
    {current?.phase==='error'?<Alert type="error" showIcon message={userReadError(current.error)} action={<Button onClick={()=>setRefresh(v=>v+1)}>重新查询</Button>}/>:<Table<UserDevice> rowKey="id" size="small" loading={current?.phase==='loading'} dataSource={value?.items??[]} scroll={{x:1110}} locale={{emptyText:<Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={query.status?'该账户暂无此状态的设备':'该账户暂无设备记录'}/>}} pagination={{current:query.page,pageSize:query.page_size,total:userPageTotal(value?.total??'0',query.page_size),showSizeChanger:true,pageSizeOptions:[10,20,50],showTotal:()=>`共 ${value?.total??'0'} 条`,onChange:(page,page_size)=>setQuery({...query,page,page_size})}} columns={[
      {title:'设备 / 编号',key:'device',width:245,render:(_,device)=><div className="device-record-name"><span className="device-platform-icon">{device.platform==='IOS'?<AppleOutlined/>:<AndroidOutlined/>}</span><div><strong>{deviceName(device)}</strong><div className="cell-sub user-id numeric">#{device.id}</div></div></div>},
      {title:'平台',dataIndex:'platform',width:90,render:(platform:UserDevice['platform'])=>platform==='IOS'?'iOS':'Android'},
      {title:'绑定状态',dataIndex:'status',width:95,render:(status:UserDevice['status'])=><Tag color={status==='ACTIVE'?'green':'default'}>{deviceStatusLabels[status]}</Tag>},
      {title:'首次绑定',dataIndex:'created_at',width:185,render:userTime},
      {title:'最后活跃记录',key:'seen',width:185,render:(_,device)=>deviceSeen(device)},
      {title:'撤销时间',key:'revoked',width:185,render:(_,device)=>deviceRevoked(device)},
      {title:'操作',key:'action',fixed:'right',width:130,render:(_,device)=><Button size="small" danger disabled={!active||!canRevokeDevice(device,session.permissions)} onClick={()=>void flow.open({userId,accountNo,device})}>{device.status==='REVOKED'?'已撤销':hasRevoke?'撤销设备':'无撤销权限'}</Button>},
    ]}/>}
    <Modal title="撤销设备" open={!!shown} onCancel={()=>flow.cancel()} footer={null} width={540} maskClosable={false} closable={shown?.phase!=='submitting'} keyboard={shown?.phase!=='submitting'} destroyOnClose>
      {shown&&<div className="device-revoke-dialog">
        <div className="device-revoke-target"><span>目标账户 <b className="numeric">{shown.target.accountNo}</b></span><strong>{deviceName(shown.target.device)}</strong><code className="numeric">设备 #{shown.target.device.id}</code><span>{shown.target.device.platform==='IOS'?'iOS':'Android'} · {deviceStatusLabels[shown.target.device.status]}</span></div>
        <Alert type="warning" showIcon message="仅撤销这台设备" description="撤销后，这台设备的登录凭据将失效。现有隧道的断开以节点执行结果为准；不会删除历史记录或影响其他设备。"/>
        {shown.error&&<Alert type={shown.phase==='observed'?'info':'error'} showIcon message={shown.error}/>}
        {(shown.phase==='loading'||shown.phase==='preparing')&&<p>正在核对设备资料…</p>}
        {shown.phase==='form'&&<form onSubmit={event=>{event.preventDefault();if(hasRevoke)void flow.prepare(reason);}}><label htmlFor="device-revoke-reason">撤销原因</label><Input id="device-revoke-reason" aria-label="撤销原因" value={reason} onChange={event=>setReason(event.target.value)} placeholder="例如：用户反馈设备遗失" maxLength={1000}/><div className="device-revoke-actions"><Button onClick={()=>flow.cancel()}>取消</Button><Button danger type="primary" htmlType="submit" disabled={!hasRevoke}>核对并继续</Button></div></form>}
        {(shown.phase==='confirm'||shown.phase==='submitting')&&<form onSubmit={event=>{event.preventDefault();if(hasRevoke)void flow.confirm(otp);}}><div className="device-revoke-reason"><span>已确认原因</span><p>{shown.reason}</p></div><label htmlFor="device-revoke-otp">动态验证码</label><Input id="device-revoke-otp" aria-label="设备撤销动态验证码" value={otp} onChange={event=>setOtp(event.target.value.replace(/\D/g,''))} maxLength={6} inputMode="numeric" autoComplete="one-time-code" disabled={shown.phase==='submitting'} placeholder={demo?'演示验证码 123456':'输入验证器中的6位数字'}/><div className="device-revoke-actions"><Button disabled={shown.phase==='submitting'} onClick={()=>flow.cancel()}>取消</Button><Button danger type="primary" htmlType="submit" loading={shown.phase==='submitting'} disabled={!hasRevoke||shown.phase==='submitting'}>确认撤销设备</Button></div></form>}
        {(shown.phase==='recovery'||shown.phase==='checking')&&<><p>查询只会读取此设备的当前状态，不会再次提交撤销。</p><div className="device-revoke-actions"><Button disabled={shown.phase==='checking'} onClick={()=>flow.cancel()}>关闭</Button><Button type="primary" loading={shown.phase==='checking'} onClick={()=>void flow.recover()}>查询当前状态</Button></div></>}
        {shown.phase==='observed'&&<Button onClick={()=>flow.cancel()}>关闭</Button>}
      </div>}
    </Modal>
    <p className="page-note">可切换“全部记录”查看撤销历史，时间均为北京时间。</p>
  </section>;
}
