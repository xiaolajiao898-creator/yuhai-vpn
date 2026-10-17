import {adminApi} from './client.ts';
import {validUserID} from './users.ts';

export type NodeStatus='ONLINE'|'OFFLINE'|'MAINTENANCE'|'DISABLED';
export type HeartbeatState='FRESH'|'STALE'|'NEVER'|'CLOCK_SKEW';
export type ManagedNode={
  id:string;name:string;group:{id:string;name:string;min_tier:number};country_code:string;city:string;region:string;
  status:NodeStatus;is_recommended:boolean;capacity_users:number;online_count:number;load_score:number;
  last_heartbeat_at:string|null;heartbeat_state:HeartbeatState;agent_version:string|null;updated_at:string;
};
export type NodeQuery={page:number;page_size:number;search?:string;status?:NodeStatus;country_code?:string};
export type NodePage={items:ManagedNode[];total:string;page:number;page_size:number;observed_at:string};
export type NodeInbound={id:string;protocol:'VLESS_REALITY'|'TROJAN'|'SS2022'|'WIREGUARD'|'HYSTERIA2';port:number;priority:number;status:'ENABLED'|'DISABLED'};
export type NodeDetail={node:ManagedNode;inbounds:NodeInbound[];observed_at:string;sync_status:'UNAVAILABLE'};
export type NodeSource={list(query:NodeQuery,signal?:AbortSignal):Promise<NodePage>;get(id:string,signal?:AbortSignal):Promise<NodeDetail>};
const invalid=()=>new Error('节点资料返回不完整，请重新查询');
const object=(v:unknown):v is Record<string,unknown>=>typeof v==='object'&&v!==null&&!Array.isArray(v);
const exact=(v:Record<string,unknown>,keys:string[])=>Object.keys(v).length===keys.length&&Object.keys(v).every(k=>keys.includes(k));
const text=(v:unknown,max:number,empty=false):v is string=>typeof v==='string'&&[...v].length<=max&&!/\p{Cc}/u.test(v)&&(empty||!!v.trim());
const integer=(v:unknown,min=0,max=2147483647):v is number=>typeof v==='number'&&Number.isInteger(v)&&v>=min&&v<=max;
const status=(v:unknown):v is NodeStatus=>typeof v==='string'&&['ONLINE','OFFLINE','MAINTENANCE','DISABLED'].includes(v);
const count=(v:unknown):v is string=>typeof v==='string'&&/^(0|[1-9][0-9]{0,18})$/.test(v)&&BigInt(v)<=9223372036854775807n;
function timestamp(v:unknown):v is string {
  if(typeof v!=='string')return false;
  const m=/^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d{1,9})?(?:Z|[+-](\d{2}):(\d{2}))$/.exec(v);
  if(!m)return false;
  const [y,mo,d,h,mi,s]=m.slice(1,7).map(Number);
  const days=[31,y%4===0&&(y%100!==0||y%400===0)?29:28,31,30,31,30,31,31,30,31,30,31];
  return y>0&&mo>=1&&mo<=12&&d>=1&&d<=days[mo-1]&&h<=23&&mi<=59&&s<=59&&Number(m[7]??0)<=23&&Number(m[8]??0)<=59&&Number.isFinite(Date.parse(v));
}
// Compare nanosecond timestamps exactly at the 90-second boundary. Date.parse
// alone truncates a future/sub-millisecond heartbeat into a false healthy state.
function nanos(value:string):bigint {
  const fraction=/\.(\d{1,9})/.exec(value)?.[1]??'';
  const whole=value.replace(/\.\d{1,9}/,'');
  return BigInt(Date.parse(whole))*1000000n+BigInt(fraction.padEnd(9,'0'));
}
export function heartbeatAt(last:string|null,observed:string):HeartbeatState {
  if(!timestamp(observed)||last!==null&&!timestamp(last))throw invalid();
  if(last===null)return 'NEVER';
  const age=nanos(observed)-nanos(last);
  return age<0n?'CLOCK_SKEW':age>90000000000n?'STALE':'FRESH';
}
export function nodeQueryProblem(q:NodeQuery):string {
  if(Object.keys(q).some(k=>!['page','page_size','search','status','country_code'].includes(k))||!integer(q.page,1,10000)||!integer(q.page_size,1,50))return '节点分页参数无效';
  if(q.search!==undefined&&(!text(q.search,64)||q.search.trim()!==q.search))return '节点名称应为1至64字';
  if(q.status!==undefined&&!status(q.status)||q.country_code!==undefined&&!/^[A-Z]{2}$/.test(q.country_code))return '节点筛选参数无效';
  return '';
}
export function readManagedNode(v:unknown,observed:string):ManagedNode {
  if(!object(v)||!exact(v,['id','name','group','country_code','city','region','status','is_recommended','capacity_users','online_count','load_score','last_heartbeat_at','heartbeat_state','agent_version','updated_at'])||
    !validUserID(v.id)||!text(v.name,64)||!object(v.group)||!exact(v.group,['id','name','min_tier'])||!validUserID(v.group.id)||!text(v.group.name,64)||!integer(v.group.min_tier,0,9)||
    typeof v.country_code!=='string'||!/^[A-Z]{2}$/.test(v.country_code)||!text(v.city,64,true)||!text(v.region,32)||!status(v.status)||typeof v.is_recommended!=='boolean'||
    !integer(v.capacity_users)||!integer(v.online_count)||!integer(v.load_score,0,100)||v.agent_version!==null&&!text(v.agent_version,32,true)||!timestamp(v.updated_at)||
    v.last_heartbeat_at!==null&&!timestamp(v.last_heartbeat_at)||v.heartbeat_state!==heartbeatAt(v.last_heartbeat_at as string|null,observed))throw invalid();
  return structuredClone(v) as ManagedNode;
}
export function readNodePage(v:unknown,q:NodeQuery):NodePage {
  if(nodeQueryProblem(q)||!object(v)||!exact(v,['items','total','page','page_size','observed_at'])||!timestamp(v.observed_at)||!Array.isArray(v.items)||!count(v.total)||v.page!==q.page||v.page_size!==q.page_size)throw invalid();
  const remaining=BigInt(v.total)-BigInt((q.page-1)*q.page_size);
  const expected=remaining<0n?0:remaining>BigInt(q.page_size)?q.page_size:Number(remaining);
  if(v.items.length!==expected)throw invalid();
  const items=v.items.map(n=>readManagedNode(n,v.observed_at as string));
  if(new Set(items.map(n=>n.id)).size!==items.length||items.some(n=>q.status&&n.status!==q.status||q.country_code&&n.country_code!==q.country_code))throw invalid();
  // Text matching/case folding is owned by PostgreSQL's locale, not JS.
  return {items,total:v.total,page:q.page,page_size:q.page_size,observed_at:v.observed_at};
}
export function readNodeDetail(v:unknown,id:string):NodeDetail {
  if(!validUserID(id)||!object(v)||!exact(v,['node','inbounds','observed_at','sync_status'])||!timestamp(v.observed_at)||v.sync_status!=='UNAVAILABLE'||!Array.isArray(v.inbounds)||v.inbounds.length>100)throw invalid();
  const node=readManagedNode(v.node,v.observed_at);
  if(node.id!==id)throw invalid();
  const inbounds=v.inbounds.map((x:unknown)=>{
    if(!object(x)||!exact(x,['id','protocol','port','priority','status'])||!validUserID(x.id)||typeof x.protocol!=='string'||!['VLESS_REALITY','TROJAN','SS2022','WIREGUARD','HYSTERIA2'].includes(x.protocol)||!integer(x.port,1,65535)||!integer(x.priority,-2147483648)||!['ENABLED','DISABLED'].includes(x.status as string))throw invalid();
    return structuredClone(x) as NodeInbound;
  });
  if(new Set(inbounds.map(x=>x.id)).size!==inbounds.length)throw invalid();
  return {node,inbounds,observed_at:v.observed_at,sync_status:'UNAVAILABLE'};
}
export const liveNodeSource:NodeSource={
  list:async(q,signal)=>{
    const problem=nodeQueryProblem(q);if(problem)throw new Error(problem);
    const params=new URLSearchParams({page:String(q.page),page_size:String(q.page_size)});
    for(const key of ['search','status','country_code'] as const)if(q[key]!==undefined)params.set(key,q[key]!);
    return readNodePage(await adminApi.get(`/nodes?${params}`,signal),q);
  },
  get:async(id,signal)=>{if(!validUserID(id))throw new Error('节点编号无效');return readNodeDetail(await adminApi.get(`/nodes/${id}`,signal),id);},
};
export async function boundedNodeRead<T>(read:(signal:AbortSignal)=>Promise<T>,signal:AbortSignal,timeoutMs=12000):Promise<T> {
  if(signal.aborted)throw new DOMException('请求已取消','AbortError');
  const controller=new AbortController();let timer:ReturnType<typeof setTimeout>|undefined;let cancel=()=>{};
  const stopped=new Promise<never>((_,reject)=>{
    cancel=()=>{controller.abort();reject(new DOMException('请求已取消','AbortError'));};
    signal.addEventListener('abort',cancel,{once:true});
    timer=setTimeout(()=>{controller.abort();reject(new Error('读取节点超时，请重新查询'));},timeoutMs);
  });
  try {
    const value=await Promise.race([Promise.resolve().then(()=>{if(controller.signal.aborted)throw new DOMException('请求已取消','AbortError');return read(controller.signal);}),stopped]);
    if(signal.aborted)throw new DOMException('请求已取消','AbortError');return value;
  }finally {if(timer!==undefined)clearTimeout(timer);signal.removeEventListener('abort',cancel);}
}
