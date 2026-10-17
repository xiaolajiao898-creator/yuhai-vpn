import {adminApi} from './client.ts';
import {validUserID} from './users.ts';

export type UserDeviceStatus = 'ACTIVE'|'REVOKED';
export type UserDevice = {
  id:string; platform:'ANDROID'|'IOS'; display_name:string|null; status:UserDeviceStatus;
  created_at:string; last_seen_at:string|null; revoked_at:string|null;
};
export type UserDeviceQuery = {page:number;page_size:number;status?:UserDeviceStatus};
export type UserDevicePage = {user_id:string;items:UserDevice[];total:string;page:number;page_size:number};
export type UserDeviceSource = {list(id:string,query:UserDeviceQuery,signal?:AbortSignal):Promise<UserDevicePage>};
const invalid=()=>new Error('设备记录返回不完整，请重新查询');
const object=(v:unknown):v is Record<string,unknown>=>typeof v==='object'&&v!==null&&!Array.isArray(v);
const keys=(v:Record<string,unknown>,allowed:string[])=>{if(Object.keys(v).length!==allowed.length||Object.keys(v).some(k=>!allowed.includes(k)))throw invalid();};
const count=(v:unknown):v is string=>typeof v==='string'&&/^(0|[1-9][0-9]{0,18})$/.test(v)&&BigInt(v)<=9223372036854775807n;
function date(v:unknown):v is string {
  if(typeof v!=='string')return false;
  const m=/^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d{1,9})?(?:Z|[+-](\d{2}):(\d{2}))$/.exec(v);
  if(!m)return false;
  const [year,month,day,hour,minute,second]=m.slice(1,7).map(Number);
  const days=[31,(year%4===0&&(year%100!==0||year%400===0))?29:28,31,30,31,30,31,31,30,31,30,31];
  return year>=1&&month>=1&&month<=12&&day>=1&&day<=days[month-1]&&hour<=23&&minute<=59&&second<=59&&Number(m[7]??0)<=23&&Number(m[8]??0)<=59&&Number.isFinite(Date.parse(v));
}
export function deviceQueryProblem(query:UserDeviceQuery):string {
  if(Object.keys(query).some(k=>!['page','page_size','status'].includes(k))||!Number.isInteger(query.page)||query.page<1||query.page>10000||!Number.isInteger(query.page_size)||query.page_size<1||query.page_size>50)return '设备分页参数无效';
  if(query.status!==undefined&&query.status!=='ACTIVE'&&query.status!=='REVOKED')return '请选择有效的设备状态';
  return '';
}
export function readUserDevice(v:unknown):UserDevice {
  if(!object(v)||!validUserID(v.id)||(v.platform!=='ANDROID'&&v.platform!=='IOS')||
    (v.display_name!==null&&(typeof v.display_name!=='string'||[...v.display_name].length>64||/\p{Cc}/u.test(v.display_name)))||
    (v.status!=='ACTIVE'&&v.status!=='REVOKED')||(v.status==='ACTIVE'&&v.revoked_at!==null)||!date(v.created_at)||(v.last_seen_at!==null&&!date(v.last_seen_at))||(v.revoked_at!==null&&!date(v.revoked_at)))throw invalid();
  keys(v,['id','platform','display_name','status','created_at','last_seen_at','revoked_at']);
  return {id:v.id,platform:v.platform,display_name:v.display_name as string|null,status:v.status,created_at:v.created_at,last_seen_at:v.last_seen_at as string|null,revoked_at:v.revoked_at as string|null};
}
export function readUserDevicePage(v:unknown,id:string,query:UserDeviceQuery):UserDevicePage {
  if(!validUserID(id)||deviceQueryProblem(query)||!object(v)||v.user_id!==id||v.page!==query.page||v.page_size!==query.page_size||!count(v.total)||!Array.isArray(v.items))throw invalid();
  keys(v,['user_id','items','total','page','page_size']);
  const remaining=BigInt(v.total)-BigInt((query.page-1)*query.page_size);
  const expected=remaining<0n?0:remaining>BigInt(query.page_size)?query.page_size:Number(remaining);
  if(v.items.length!==expected)throw invalid();
  const items=v.items.map(readUserDevice);
  if(new Set(items.map(d=>d.id)).size!==items.length||items.some(d=>query.status&&d.status!==query.status))throw invalid();
  return {user_id:id,items,total:v.total,page:query.page,page_size:query.page_size};
}
export const liveUserDeviceSource:UserDeviceSource={list:async(id,query,signal)=>{
  if(!validUserID(id))throw new Error('用户编号无效');
  const problem=deviceQueryProblem(query);if(problem)throw new Error(problem);
  const params=new URLSearchParams({page:String(query.page),page_size:String(query.page_size),...(query.status?{status:query.status}:{})});
  return readUserDevicePage(await adminApi.get(`/users/${id}/devices?${params}`,signal),id,query);
}};

// A bounded read aborts its transport on timeout or parent cancellation. It never
// retries, and still settles when a transport ignores AbortSignal.
export async function loadUserDevices(source:UserDeviceSource,id:string,query:UserDeviceQuery,signal:AbortSignal,timeoutMs=12000):Promise<UserDevicePage> {
  if(signal.aborted)throw new DOMException('请求已取消','AbortError');
  const controller=new AbortController();let timer:ReturnType<typeof setTimeout>|undefined;
  let cancel:()=>void=()=>{};
  const stopped=new Promise<never>((_,reject)=>{
    cancel=()=>{controller.abort();reject(new DOMException('请求已取消','AbortError'));};
    signal.addEventListener('abort',cancel,{once:true});
    timer=setTimeout(()=>{controller.abort();reject(new Error('读取设备超时，请重新查询'));},timeoutMs);
  });
  try {
    const raw=await Promise.race([Promise.resolve().then(()=>{if(controller.signal.aborted)throw new DOMException('请求已取消','AbortError');return source.list(id,query,controller.signal);}),stopped]);
    if(signal.aborted)throw new DOMException('请求已取消','AbortError');
    return readUserDevicePage(raw,id,query);
  }finally{if(timer!==undefined)clearTimeout(timer);signal.removeEventListener('abort',cancel);}
}
