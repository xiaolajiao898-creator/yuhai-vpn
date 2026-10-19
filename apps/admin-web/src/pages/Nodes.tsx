import {useCallback,useEffect,useMemo,useState} from 'react';
import {Alert,Button,Descriptions,Drawer,Empty,Input,InputNumber,Progress,Select,Space,Spin,Table,Tag} from 'antd';
import {CloudServerOutlined,GlobalOutlined,ReadOutlined,ReloadOutlined,SearchOutlined} from '@ant-design/icons';
import type {AdminSession} from '../api/client';
import {boundedNodeRead,nodeQueryProblem,readNodeDetail,readNodePage,type ManagedNode,type NodeDetail,type NodePage,type NodeQuery,type NodeSource,type NodeStatus,type HeartbeatState} from '../api/nodes';
import {createLatestRead,stateForRead,type ReadState} from './latest-read';
import {userPageTotal,userTime} from './users-model';
import './nodes.css';

const statusLabels:Record<NodeStatus,string>={ONLINE:'在线配置',OFFLINE:'离线',MAINTENANCE:'维护中',DISABLED:'已停用'};
const heartbeatLabels:Record<HeartbeatState,string>={FRESH:'心跳正常',STALE:'心跳超时',NEVER:'尚无心跳',CLOCK_SKEW:'时间异常'};
const heartbeatColors:Record<HeartbeatState,string>={FRESH:'green',STALE:'orange',NEVER:'default',CLOCK_SKEW:'red'};
const countryLabels:Record<string,string>={HK:'香港',JP:'日本',SG:'新加坡',US:'美国',DE:'德国',TW:'台湾',KR:'韩国',GB:'英国',CA:'加拿大',AU:'澳大利亚'};
const regionLabels:Record<string,string>={ASIA:'亚洲',EUROPE:'欧洲',AMERICAS:'美洲',OCEANIA:'大洋洲',AFRICA:'非洲'};
function CapacityEditor({source,node,canWrite,onSaved}:{source:NodeSource;node:ManagedNode;canWrite:boolean;onSaved:()=>void}){
  const [value,setValue]=useState(node.capacity_users),[busy,setBusy]=useState(false),[error,setError]=useState('');
  if(!canWrite)return <span className="numeric">{node.online_count} / {node.capacity_users}</span>;
  return <div>
    <Space wrap>
      <InputNumber min={0} max={1000000} value={value} onChange={v=>setValue(Number(v??0))}/>
      <Button loading={busy} onClick={()=>{setBusy(true);setError('');source.updateCapacity(node.id,value).then(()=>onSaved()).catch(e=>setError(e instanceof Error?e.message:'没有保存')).finally(()=>setBusy(false));}}>保存容量</Button>
    </Space>
    <div className="cell-sub">最近上报 {node.last_heartbeat_at===null?'—':node.online_count} 人。0 表示不按连接数判满。改的是登记上限，不会断开已有连接。</div>
    {error&&<Alert type="error" showIcon message={error} style={{marginTop:8}}/>}
  </div>;
}
function useNodeRead<T>(key:string|undefined,read:(signal:AbortSignal)=>Promise<T>){
  const [state,setState]=useState<ReadState<T>>();
  const channel=useMemo(()=>createLatestRead<T>(setState),[]);
  useEffect(()=>{if(key===undefined){channel.cancel();return;}void channel.start(key,signal=>boundedNodeRead(read,signal));return channel.cancel;},[key,read,channel]);
  return stateForRead(state,key);
}
function ReadError({error,retry}:{error:unknown;retry:()=>void}){
  const status=(error as {status?:number})?.status;
  const message=status===401?'登录已失效，请重新登录':status===403?'当前账号没有节点查看权限':status===404?'节点记录不存在':error instanceof Error?error.message:'节点资料暂不可用';
  return <Alert type="error" showIcon message={message} action={<Button onClick={retry}>重新查询</Button>}/>;
}
let scopeSequence=0;
export function NodesPage({source,session}:{source:NodeSource;session:AdminSession}) {
  const canRead=session.permissions.includes('nodes.read');
  const scope=useMemo(()=>++scopeSequence,[source,session.id,canRead]);
  const [query,setQuery]=useState<NodeQuery>({page:1,page_size:20});
  const [input,setInput]=useState(''),[country,setCountry]=useState(''),[problem,setProblem]=useState('');
  const [refresh,setRefresh]=useState(0),[selected,setSelected]=useState<string>(),[detailRefresh,setDetailRefresh]=useState(0);
  const readList=useCallback(async(signal:AbortSignal)=>readNodePage(await source.list(query,signal),query),[source,query]);
  const list=useNodeRead<NodePage>(canRead?`${scope}:${JSON.stringify(query)}:${refresh}`:undefined,readList);
  const readDetail=useCallback(async(signal:AbortSignal)=>readNodeDetail(await source.get(selected!,signal),selected!),[source,selected]);
  const details=useNodeRead<NodeDetail>(canRead&&selected?`${scope}:${selected}:${detailRefresh}`:undefined,readDetail);
  const data=list?.phase==='ready'?list.value:undefined;
  const detail=details?.phase==='ready'?details.value:undefined;
  const items=data?.items??[];
  const healthy=items.filter(n=>n.status==='ONLINE'&&n.heartbeat_state==='FRESH').length;
  const attention=items.filter(n=>n.status==='ONLINE'&&n.heartbeat_state!=='FRESH').length;
  function search(){const next={...query,page:1,search:input.trim()||undefined,country_code:country.trim().toUpperCase()||undefined};const error=nodeQueryProblem(next);setProblem(error);if(!error)setQuery(next);}
  if(!canRead)return <Alert type="warning" showIcon message="当前账号没有节点查看权限"/>;
  return <div className="nodes-page">
    <div className="page-intro"><div><div className="eyebrow">NETWORK OVERVIEW</div><h1>节点管理</h1><p>线路状态与心跳记录，在这里一目了然。</p></div><Button icon={<ReloadOutlined/>} loading={list?.phase==='loading'} onClick={()=>setRefresh(x=>x+1)}>刷新节点</Button></div>
    <section className="node-hero"><div className="node-hero-copy"><span className="node-orbit"><GlobalOutlined/></span><div><strong>连接世界，从每一条线路开始</strong><p>查看线路分布、上报负载与入站协议。</p></div></div><Tag icon={<ReadOutlined/>}>只读巡检</Tag></section>
    <div className="node-metrics">
      <section><span>符合筛选的节点</span><strong className="numeric">{data?.total??'—'}</strong><small>全部分页的记录总量</small></section>
      <section><span>本页在线且心跳正常</span><strong className="numeric good">{data?healthy:'—'}</strong><small>最近90秒内收到心跳</small></section>
      <section><span>本页在线配置需检查</span><strong className="numeric caution">{data?attention:'—'}</strong><small>心跳缺失、超时或时间异常</small></section>
    </div>
    <section className="surface spaced">
      <form className="node-filters" onSubmit={e=>{e.preventDefault();search();}}>
        <label className="node-name-filter">节点名称<Input aria-label="查询节点名称" prefix={<SearchOutlined/>} value={input} onChange={e=>setInput(e.target.value)} placeholder="搜索线路名称" allowClear maxLength={128}/></label>
        <label>地区代码<Input aria-label="节点地区代码" value={country} onChange={e=>setCountry(e.target.value)} placeholder="例如 HK、JP" maxLength={2} allowClear/></label>
        <label>配置状态<Select aria-label="节点状态筛选" value={query.status??''} options={[{value:'',label:'全部状态'},...Object.entries(statusLabels).map(([value,label])=>({value,label}))]} onChange={value=>setQuery({...query,page:1,status:(value||undefined) as NodeStatus|undefined})}/></label>
        <Button type="primary" htmlType="submit">查询</Button><Button onClick={()=>{setInput('');setCountry('');setProblem('');setQuery({page:1,page_size:query.page_size});}}>重置</Button>
      </form>
      {problem&&<div className="inset"><Alert showIcon type="error" message={problem}/></div>}
      <div className="table-toolbar"><strong>线路列表</strong><span>{data?`数据读取于 ${userTime(data.observed_at)}`:'正在读取节点资料'}</span></div>
      {list?.phase==='error'?<div className="inset"><ReadError error={list.error} retry={()=>setRefresh(x=>x+1)}/></div>:<Table<ManagedNode> rowKey="id" loading={list?.phase==='loading'} dataSource={items} scroll={{x:1130}} locale={{emptyText:<Empty description="暂无符合条件的节点"/>}} pagination={{current:query.page,pageSize:query.page_size,total:userPageTotal(data?.total??'0',query.page_size),showSizeChanger:true,pageSizeOptions:[10,20,50],showTotal:()=>`共 ${data?.total??'0'} 条线路`,onChange:(page,page_size)=>setQuery({...query,page:page_size===query.page_size?page:1,page_size})}} columns={[
        {title:'节点 / 地区',key:'name',width:235,render:(_,n)=><div className="node-name-cell"><span className="node-country">{n.country_code}</span><div><Button type="link" className="node-title" onClick={()=>setSelected(n.id)}>{n.name}</Button><div className="cell-sub">{countryLabels[n.country_code]??n.country_code} · {n.city||'未填写城市'} {n.is_recommended&&<span className="node-recommend">推荐</span>}</div></div></div>},
        {title:'分组',key:'group',width:130,render:(_,n)=><><strong>{n.group.name}</strong><div className="cell-sub">等级 <span className="numeric">{n.group.min_tier}</span> 起</div></>},
        {title:'配置状态',dataIndex:'status',width:110,render:(s:NodeStatus)=><Tag color={s==='ONLINE'?'blue':s==='MAINTENANCE'?'gold':'default'}>{statusLabels[s]}</Tag>},
        {title:'心跳情况',key:'heartbeat',width:200,render:(_,n)=><><Tag color={heartbeatColors[n.heartbeat_state]}>{heartbeatLabels[n.heartbeat_state]}</Tag><div className="cell-sub">{n.last_heartbeat_at?userTime(n.last_heartbeat_at):'从未收到上报'}</div></>},
        {title:'最近上报负载',key:'load',width:140,render:(_,n)=>n.last_heartbeat_at===null?<span className="muted">暂无上报</span>:<div className={n.heartbeat_state==='FRESH'?'':'node-old-metric'}><Progress percent={n.load_score} size="small" strokeColor="#658b60" format={v=><span className="numeric">{v}%</span>}/></div>},
        {title:'最近上报连接 / 容量',key:'capacity',width:170,render:(_,n)=><span className="numeric">{n.last_heartbeat_at===null?'—':n.online_count} / {n.capacity_users}</span>},
        {title:'',key:'detail',fixed:'right',width:95,render:(_,n)=><Button type="link" onClick={()=>setSelected(n.id)}>查看详情</Button>},
      ]}/>}
    </section><p className="page-note">心跳与负载是读取时的上报快照，不代表实际隧道已通过连通验证。刷新可获取最新记录。</p>
    <Drawer title="节点详情" open={!!selected} onClose={()=>setSelected(undefined)} width={800} destroyOnClose extra={<Button icon={<ReloadOutlined/>} loading={details?.phase==='loading'} onClick={()=>setDetailRefresh(x=>x+1)}>刷新资料</Button>}>
      {details?.phase==='loading'?<div className="node-detail-loading"><Spin/><p>正在读取节点资料</p></div>:details?.phase==='error'?<ReadError error={details.error} retry={()=>setDetailRefresh(x=>x+1)}/>:detail&&<>
        <div className="node-detail-heading"><span className="node-orbit"><CloudServerOutlined/></span><div><div className="eyebrow">NODE PROFILE</div><h2>{detail.node.name}</h2><span className="numeric muted">#{detail.node.id}</span></div><Tag color={heartbeatColors[detail.node.heartbeat_state]}>{heartbeatLabels[detail.node.heartbeat_state]}</Tag></div>
        <Descriptions bordered column={1} size="small" items={[
          {key:'region',label:'线路位置',children:`${countryLabels[detail.node.country_code]??detail.node.country_code} · ${detail.node.city||'未填写城市'} · ${regionLabels[detail.node.region]??detail.node.region}`},
          {key:'group',label:'节点分组',children:`${detail.node.group.name} · 等级 ${detail.node.group.min_tier} 起`},
          {key:'status',label:'配置状态',children:statusLabels[detail.node.status]},
          {key:'heartbeat',label:'最后心跳',children:detail.node.last_heartbeat_at?userTime(detail.node.last_heartbeat_at):'尚无心跳记录'},
          {key:'capacity',label:'连接容量',children:<CapacityEditor source={source} node={detail.node} canWrite={session.permissions.includes('nodes.write')} onSaved={()=>setDetailRefresh(x=>x+1)}/>},
          {key:'agent',label:'节点程序版本',children:detail.node.agent_version||'尚无版本记录'},
          {key:'updated',label:'资料更新',children:userTime(detail.node.updated_at)},
          {key:'observed',label:'本次读取',children:userTime(detail.observed_at)},
        ]}/>
        <h3 className="node-section-heading">入站协议</h3><Table rowKey="id" size="small" dataSource={detail.inbounds} pagination={false} scroll={{x:460}} locale={{emptyText:'尚未配置入站'}} columns={[
          {title:'协议',dataIndex:'protocol'},{title:'端口',dataIndex:'port',render:(v:number)=><span className="numeric">{v}</span>},{title:'优先级',dataIndex:'priority',render:(v:number)=><span className="numeric">{v}</span>},{title:'状态',dataIndex:'status',render:(v:string)=><Tag color={v==='ENABLED'?'green':'default'}>{v==='ENABLED'?'已启用':'已停用'}</Tag>},
        ]}/>
        <h3 className="node-section-heading">设备同步</h3><Alert type="info" showIcon message="节点执行回执尚未接入" description="目前不能在这里确认设备撤销是否已在该节点执行。后台撤销记录与实际连接断开需要分别核实。"/>
      </>}
    </Drawer>
  </div>;
}
