import {useCallback,useEffect,useMemo,useState} from 'react';
import {Alert,Button,Descriptions,Drawer,Empty,Input,Select,Spin,Table,Tabs,Tag} from 'antd';
import {ArrowDownOutlined,ArrowUpOutlined,ReadOutlined,ReloadOutlined,SearchOutlined,TeamOutlined,WalletOutlined} from '@ant-design/icons';
import type {AdminSession} from '../api/client';
import type {Page} from '../api/review';
import type {BalanceEntry,BalancePage,EntitlementEntry,EntitlementPage,LedgerQuery,ManagedUser,ManagedUserDetail,UserQuery,UserSource,UserStatus} from '../api/users';
import {userQueryProblem} from '../api/users';
import type {DeviceRevokeSource} from '../api/device-revoke';
import type {UserDeviceSource} from '../api/user-devices';
import {UserDevices} from './UserDevices';
import {createLatestRead,stateForRead,type ReadState} from './latest-read';
import {entryTypeLabels,entitlementTypeLabels,entitlementSourceLabels,entitlementDelta,type EntitlementUnit,formatBytes,formatUSDT,operatorLabels,sourceTypeLabels,userPageTotal,userReadError,userStatusLabels,userTime} from './users-model';

function useUserRead<T>(key:string|undefined,read:(signal:AbortSignal)=>Promise<T>) {
  const [state,setState]=useState<ReadState<T>>();
  const channel=useMemo(()=>createLatestRead<T>(setState),[]);
  useEffect(()=>{
    if(key===undefined){channel.cancel();return;}
    void channel.start(key,read);
    return channel.cancel;
  },[key,read,channel]);
  return stateForRead(state,key);
}
let scopeSequence=0;
const statusColor=(status:UserStatus)=>status==='ACTIVE'?'green':status==='SUSPENDED'?'gold':'red';

export function UsersPage({source,session,devices,revocations,demo=false}:{source:UserSource;session:AdminSession;devices:UserDeviceSource;revocations:DeviceRevokeSource;demo?:boolean}) {
  const [query,setQuery]=useState<UserQuery>({page:1,page_size:20});
  const [accountInput,setAccountInput]=useState(''),[filterError,setFilterError]=useState('');
  const [refresh,setRefresh]=useState(0),[detailRefresh,setDetailRefresh]=useState(0);
  const [deviceFeedback,setDeviceFeedback]=useState('');
  const [selected,setSelected]=useState<string>();
  const [tab,setTab]=useState('overview');
  const [ledgerQuery,setLedgerQuery]=useState<LedgerQuery>({page:1,page_size:20});
  const [entitlementQuery,setEntitlementQuery]=useState<LedgerQuery>({page:1,page_size:20});
  const canRead=session.permissions.includes('users.read');
  const canReadLedger=canRead&&session.permissions.includes('users.ledgers.read');
  const scope=useMemo(()=>++scopeSequence,[source,session.id,canRead,canReadLedger]);
  const listRead=useCallback((signal:AbortSignal)=>source.list(query,signal),[source,query]);
  const list=useUserRead<Page<ManagedUser>>(canRead?`${scope}:users:${JSON.stringify(query)}:${refresh}`:undefined,listRead);
  const detailRead=useCallback((signal:AbortSignal)=>source.get(selected!,signal),[source,selected]);
  const detailState=useUserRead<ManagedUserDetail>(canRead&&selected?`${scope}:user:${selected}:${detailRefresh}`:undefined,detailRead);
  const detail=detailState?.phase==='ready'?detailState.value:undefined;
  const ledgerRead=useCallback((signal:AbortSignal)=>source.ledgers(selected!,ledgerQuery,signal),[source,selected,ledgerQuery]);
  const ledgers=useUserRead<BalancePage>(canReadLedger&&selected&&detail?.id===selected&&tab==='balance'?`${scope}:balance:${selected}:${JSON.stringify(ledgerQuery)}:${detailRefresh}`:undefined,ledgerRead);
  const entitlementRead=useCallback((signal:AbortSignal)=>source.entitlements(selected!,entitlementQuery,signal),[source,selected,entitlementQuery]);
  const entitlementRows=useUserRead<EntitlementPage>(canReadLedger&&selected&&detail?.id===selected&&tab==='entitlement'?`${scope}:entitlement:${selected}:${JSON.stringify(entitlementQuery)}:${detailRefresh}`:undefined,entitlementRead);
  function openUser(id:string){setDeviceFeedback('');setSelected(id);setDetailRefresh(v=>v+1);setTab('overview');setLedgerQuery({page:1,page_size:20});setEntitlementQuery({page:1,page_size:20});}
  function search(){const next={...query,account_no:accountInput.trim()||undefined,page:1};const problem=userQueryProblem(next);setFilterError(problem);if(!problem)setQuery(next);}
  if(!canRead)return <Alert type="warning" showIcon message="当前账号没有用户管理的查看权限" description="请联系管理员开通查看权限。"/>;
  const listed=list?.phase==='ready'?list.value:undefined;
  const refreshDetail=()=>setDetailRefresh(v=>v+1);
  return <div className="users-page">
    <div className="page-intro"><div><div className="eyebrow">ACCOUNT DIRECTORY</div><h1>用户管理</h1><p>查看账户资料、绑定设备及余额与权益变动。</p></div><Button icon={<ReloadOutlined/>} loading={list?.phase==='loading'} onClick={()=>setRefresh(v=>v+1)}>刷新列表</Button></div>
    <div className="users-guide"><div className="users-guide-icon"><TeamOutlined/></div><div><strong>账户资料，一处查阅</strong><p>输入完整账户号，快速找到对应账户。</p></div><Tag icon={<ReadOutlined/>}>{session.permissions.includes('devices.revoke')?'按权限操作':'只读查看'}</Tag></div>
    <section className="surface spaced">
      <div className="users-search-panel"><form onSubmit={e=>{e.preventDefault();search();}}><label htmlFor="users-account-search">账户号</label><div className="users-search-input"><Input id="users-account-search" aria-label="查询账户号" prefix={<SearchOutlined/>} value={accountInput} onChange={e=>{setAccountInput(e.target.value);setFilterError('');}} placeholder="输入完整的10位账户号" inputMode="numeric" maxLength={32} allowClear/><Button type="primary" htmlType="submit">查询</Button></div></form><label className="users-status-filter">账户状态<Select aria-label="账户状态筛选" value={query.status??''} options={[{value:'',label:'全部状态'},...Object.entries(userStatusLabels).map(([value,label])=>({value,label}))]} onChange={value=>setQuery({...query,status:value==='ACTIVE'||value==='SUSPENDED'||value==='BANNED'?value:undefined,page:1})}/></label><Button onClick={()=>{setAccountInput('');setFilterError('');setQuery({page:1,page_size:query.page_size});}}>重置</Button></div>
      {filterError&&<div className="users-filter-error"><Alert type="error" showIcon message={filterError}/></div>}
      <div className="table-toolbar"><strong>账户列表 <span className="users-record-count numeric">{listed?.total??'—'}</span></strong><div className="users-applied-filters">{query.account_no&&<Tag className="numeric">账户 {query.account_no}</Tag>}{query.status&&<Tag color={statusColor(query.status)}>{userStatusLabels[query.status]}</Tag>}<span>北京时间</span></div></div>
      {list?.phase==='error'?<div className="inset"><ReadError error={list.error} retry={()=>setRefresh(v=>v+1)}/></div>:<Table<ManagedUser> rowKey="id" loading={list?.phase==='loading'} dataSource={listed?.items??[]} scroll={{x:970}} locale={{emptyText:<Empty description={query.account_no||query.status?'没有符合条件的账户':'暂无账户记录'}/>}} pagination={{current:query.page,pageSize:query.page_size,total:userPageTotal(listed?.total??'0',query.page_size),showSizeChanger:true,pageSizeOptions:[10,20,50],showTotal:()=>`共 ${listed?.total??'0'} 个账户`,onChange:(page,page_size)=>setQuery({...query,page,page_size})}} columns={[
        {title:'账户',key:'account',width:245,render:(_,user)=><div className="user-account-cell"><span className="user-account-icon"><TeamOutlined/></span><div><strong className="numeric">{user.account_no}</strong><div className="cell-sub user-id numeric">用户 #{user.id}</div></div></div>},
        {title:'可用余额 · USDT',dataIndex:'balance',width:210,render:(balance:string|null)=><span className={balance===null?'muted':'numeric user-table-amount'}>{balance===null?'暂无记录':formatUSDT(balance)}</span>},
        {title:'状态',dataIndex:'status',width:105,render:(status:UserStatus)=><Tag color={statusColor(status)}>{userStatusLabels[status]}</Tag>},
        {title:'已绑定设备',dataIndex:'active_device_count',width:110,render:(count:string)=><span className="numeric">{count}</span>},
        {title:'注册时间',dataIndex:'created_at',width:190,render:userTime},
        {title:'',key:'detail',fixed:'right',width:110,render:(_,user)=><Button type="link" onClick={()=>openUser(user.id)}>查看详情</Button>},
      ]}/>}
    </section>
    <p className="page-note">账户号与用户编号均保留完整数字。余额没有记录时显示“暂无记录”。</p>
    <Drawer title="用户详情" open={!!selected} onClose={()=>setSelected(undefined)} width={880} destroyOnClose extra={<Button icon={<ReloadOutlined/>} onClick={refreshDetail} loading={detailState?.phase==='loading'}>刷新资料</Button>}>
      {deviceFeedback&&<Alert className="device-change-feedback" showIcon type="info" message={deviceFeedback}/> }
      {detailState?.phase==='loading'?<div className="user-detail-loading"><Spin/><p>正在读取账户资料</p></div>:detailState?.phase==='error'?<ReadError error={detailState.error} retry={refreshDetail}/>:detail&&<>
        <div className="user-detail-heading"><div><div className="eyebrow">ACCOUNT PROFILE</div><h2 className="numeric">{detail.account_no}</h2><span className="muted user-id">用户编号 <span className="numeric">{detail.id}</span></span></div><Tag color={statusColor(detail.status)}>{userStatusLabels[detail.status]}</Tag></div>
        <div className="user-metrics"><section className="user-balance-card"><div><WalletOutlined/> 可用余额</div><strong className="numeric">{formatUSDT(detail.balance)} <small>USDT</small></strong><p>{detail.balance===null?'暂无余额记录':`更新于 ${userTime(detail.balance_updated_at)}`}</p></section><section className="user-device-card"><span>已绑定设备</span><strong className="numeric">{detail.active_device_count}<small>台</small></strong><p>当前未撤销的设备记录</p></section></div>
        <Tabs className="user-detail-tabs" activeKey={tab} onChange={setTab} items={[
          {key:'overview',label:'账户资料',children:<UserOverview detail={detail}/>},
          {key:'devices',label:'绑定设备',children:<UserDevices source={devices} revocations={revocations} session={session} userId={detail.id} accountNo={detail.account_no} active={tab==='devices'} scope={`${scope}:${detailRefresh}`} demo={demo} onChanged={text=>{setDeviceFeedback(text);setRefresh(v=>v+1);setDetailRefresh(v=>v+1);}}/>},
          {key:'balance',label:'余额流水',children:!canReadLedger?<Alert type="warning" showIcon message="当前账号没有余额流水查看权限" description="账户资料仍可正常查看。"/>:<BalanceRecords state={ledgers} query={ledgerQuery} onPage={(page,page_size)=>setLedgerQuery({page,page_size})} retry={refreshDetail}/>},
          {key:'entitlement',label:'权益流水',children:!canReadLedger?<Alert type="warning" showIcon message="当前账号没有权益流水查看权限" description="账户资料与已获授权的标签仍可查看。"/>:<EntitlementRecords state={entitlementRows} query={entitlementQuery} onPage={(page,page_size)=>setEntitlementQuery({page,page_size})} retry={refreshDetail}/>},
        ]}/>
      </>}
    </Drawer>
  </div>;
}
function ReadError({error,retry}:{error:unknown;retry:()=>void}) {return <Alert type="error" showIcon message={userReadError(error)} action={<Button onClick={retry}>重新查询</Button>}/>;}
function UserOverview({detail}:{detail:ManagedUserDetail}) {
  const subscription=detail.subscription;
  return <><h3 className="user-section-heading">基础资料</h3><Descriptions className="user-profile-facts" column={1} size="small" bordered items={[
    {key:'invite',label:'公开邀请码',children:<span className="numeric">{detail.invite_code}</span>},
    {key:'registered',label:'注册时间',children:userTime(detail.created_at)},
    {key:'updated',label:'资料更新时间',children:userTime(detail.updated_at)},
  ]}/><div className="user-section-heading"><h3>套餐记录</h3><span>不包含额外流量包和设备扩展</span></div>
    {!subscription?<div className="user-empty-subscription"><Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无套餐记录"/></div>:<>
      <Descriptions className="user-profile-facts" bordered column={1} size="small" items={[
        {key:'plan',label:'套餐编号',children:subscription.plan_id?<span className="numeric">#{subscription.plan_id}</span>:'未关联套餐'},
        {key:'tier',label:'套餐等级',children:<span className="numeric">{subscription.tier}</span>},
        {key:'expiry',label:'到期时间',children:subscription.expires_at?userTime(subscription.expires_at):'未设置到期时间'},
        {key:'devices',label:'套餐设备名额',children:<span className="numeric">{subscription.device_limit} 台</span>},
        {key:'quota',label:'周期流量上限',children:subscription.period_quota_bytes===null?'未设周期上限':<ByteValue value={subscription.period_quota_bytes}/>},
        {key:'used',label:'周期已用流量',children:<ByteValue value={subscription.period_used_bytes}/>},
        {key:'reset',label:'周期重置时间',children:subscription.period_reset_at?userTime(subscription.period_reset_at):'未设置'},
      ]}/><p className="page-note">这里展示已保存的套餐资料；到期记录可能仍然保留，不代表当前可以连接。</p>
    </>}
  </>;
}
function ByteValue({value}:{value:string}) {return <span className="user-byte-value"><strong className="numeric">{formatBytes(value)}</strong><small className="numeric">{value} 字节</small></span>;}
function BalanceRecords({state,query,onPage,retry}:{state:ReadState<BalancePage>|undefined;query:LedgerQuery;onPage:(page:number,size:number)=>void;retry:()=>void}) {
  const value=state?.phase==='ready'?state.value:undefined;
  if(state?.phase==='error')return <ReadError error={state.error} retry={retry}/>;
  return <div className="user-ledger"><div className="user-ledger-caption"><strong>余额变动</strong><span>共 <b className="numeric">{value?.total??'—'}</b> 笔 · USDT</span></div><p>每笔余额为该次变动后的记录，时间均为北京时间。</p><Table<BalanceEntry> rowKey="id" size="small" loading={state?.phase==='loading'} dataSource={value?.items??[]} scroll={{x:1030}} locale={{emptyText:<Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="该账户暂无余额流水"/>}} pagination={{current:query.page,pageSize:query.page_size,total:userPageTotal(value?.total??'0',query.page_size),showSizeChanger:true,pageSizeOptions:[10,20,50],showTotal:()=>`共 ${value?.total??'0'} 笔`,onChange:onPage}} columns={[
    {title:'类型 / 流水编号',key:'type',width:215,render:(_,entry)=><div><strong>{entryTypeLabels[entry.entry_type]}</strong><div className="cell-sub user-id numeric">#{entry.id}</div></div>},
    {title:'变动金额 · USDT',key:'amount',width:215,render:(_,entry)=><span className={`numeric user-ledger-amount ${entry.amount.startsWith('-')?'is-debit':'is-credit'}`}>{entry.amount.startsWith('-')?<ArrowUpOutlined/>:<ArrowDownOutlined/>}{formatUSDT(entry.amount,true)}</span>},
    {title:'变动后余额 · USDT',dataIndex:'balance_after',width:205,render:(amount:string)=><span className="numeric user-table-amount">{formatUSDT(amount)}</span>},
    {title:'来源',key:'source',width:195,render:(_,entry)=><div>{sourceTypeLabels[entry.source_type]}<div className="cell-sub user-id numeric">#{entry.source_id}</div></div>},
    {title:'操作方',dataIndex:'operator_type',width:85,render:(operator:BalanceEntry['operator_type'])=>operatorLabels[operator]},
    {title:'时间',dataIndex:'created_at',width:180,render:userTime},
  ]}/></div>;
}

function EntitlementRecords({state,query,onPage,retry}:{state:ReadState<EntitlementPage>|undefined;query:LedgerQuery;onPage:(page:number,size:number)=>void;retry:()=>void}) {
  const value=state?.phase==='ready'?state.value:undefined;
  if(state?.phase==='error')return <ReadError error={state.error} retry={retry}/>;
  return <div className="user-ledger user-entitlement-ledger"><div className="user-ledger-caption"><strong>权益变动</strong><span>共 <b className="numeric">{value?.total??'—'}</b> 笔</span></div><p>“+”表示增加，“-”表示减少，0表示该项无变化。这里记录每次变动，不代表当前剩余额度。</p><div className="entitlement-units-note">流量按1024进制换算为 KiB / MiB / GiB，并保留精确字节数。时间为北京时间。</div><Table<EntitlementEntry> rowKey="id" size="small" loading={state?.phase==='loading'} dataSource={value?.items??[]} scroll={{x:1220}} locale={{emptyText:<Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="该账户暂无权益流水"/>}} pagination={{current:query.page,pageSize:query.page_size,total:userPageTotal(value?.total??'0',query.page_size),showSizeChanger:true,pageSizeOptions:[10,20,50],showTotal:()=>`共 ${value?.total??'0'} 笔`,onChange:onPage}} columns={[
    {title:'类型 / 流水编号',key:'type',width:200,render:(_,entry)=><div><strong>{entitlementTypeLabels[entry.change_type]}</strong><div className="cell-sub user-id numeric">#{entry.id}</div></div>},
    {title:'时长变动',dataIndex:'delta_seconds',width:220,render:(value:string)=><EntitlementChange value={value} unit="seconds"/>},
    {title:'流量变动',dataIndex:'delta_bytes',width:240,render:(value:string)=><EntitlementChange value={value} unit="bytes"/>},
    {title:'设备名额变动',dataIndex:'delta_devices',width:145,render:(value:string)=><EntitlementChange value={value} unit="devices"/>},
    {title:'来源',key:'source',width:175,render:(_,entry)=><div>{entitlementSourceLabels[entry.source_type]}<div className="cell-sub user-id">{entry.source_id===null?'无关联编号':<span className="numeric">#{entry.source_id}</span>}</div></div>},
    {title:'操作方',dataIndex:'operator_type',width:85,render:(operator:EntitlementEntry['operator_type'])=>operatorLabels[operator]},
    {title:'时间',dataIndex:'created_at',width:180,render:userTime},
  ]}/></div>;
}
function EntitlementChange({value,unit}:{value:string;unit:EntitlementUnit}) {
  const result=entitlementDelta(value,unit);
  return <span className={`entitlement-change is-${result.direction}`}><strong className="numeric">{result.label}</strong>{result.exact&&<small className="numeric">{result.exact}</small>}{result.direction==='zero'&&<small>无变化</small>}</span>;
}
