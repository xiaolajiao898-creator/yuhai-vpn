import {adminApi} from './client.ts';
import type {Page} from './review.ts';

export type UserStatus = 'ACTIVE' | 'SUSPENDED' | 'BANNED';
export type ManagedUser = {
  id:string; account_no:string; status:UserStatus; balance:string|null; currency:'USDT';
  active_device_count:string; created_at:string; updated_at:string;
};
export type SubscriptionRecord = {
  plan_id:string|null; tier:number; expires_at:string|null; device_limit:number;
  period_quota_bytes:string|null; period_used_bytes:string; period_reset_at:string|null;
};
export type ManagedUserDetail = ManagedUser & {invite_code:string; balance_updated_at:string|null; subscription:SubscriptionRecord|null};
export type BalanceEntryType = 'DEPOSIT'|'PURCHASE'|'INVITE_REWARD'|'EXCEPTION_CREDIT'|'FULFILL_FALLBACK'|'ADMIN_CREDIT'|'ADMIN_DEBIT';
export type BalanceSourceType = 'CHAIN_TRANSFER'|'ORDER'|'INVITE_REWARD'|'DEPOSIT_EXCEPTION'|'ADMIN_APPROVAL';
export type BalanceEntry = {
  id:string; entry_type:BalanceEntryType; amount:string; balance_after:string;
  source_type:BalanceSourceType; source_id:string; operator_type:'USER'|'ADMIN'|'SYSTEM'; created_at:string;
};
export type EntitlementChangeType = 'PURCHASE'|'RENEW'|'TIER_CHANGE'|'TRAFFIC_PACK'|'DEVICE_ADDON'|'INVITE_REWARD'|'ADMIN_ADJUST'|'TRAFFIC_CONSUME_OVERFLOW'|'PERIOD_RESET'|'EXPIRE'|'REVOKE';
export type EntitlementSourceType = 'ORDER'|'INVITE_REWARD'|'ADMIN_APPROVAL'|'SYSTEM';
export type EntitlementEntry = {
  id:string; change_type:EntitlementChangeType; delta_seconds:string; delta_bytes:string; delta_devices:string;
  source_type:EntitlementSourceType; source_id:string|null; operator_type:'USER'|'ADMIN'|'SYSTEM'; created_at:string;
};
export type EntitlementPage = Page<EntitlementEntry> & {user_id:string; type:'entitlement'; currency:null};
export type UserQuery = {page:number; page_size:number; account_no?:string; status?:UserStatus};
export type LedgerQuery = {page:number; page_size:number};
export type BalancePage = Page<BalanceEntry> & {user_id:string; type:'balance'; currency:'USDT'};
export type UserSource = {
  list(query:UserQuery,signal?:AbortSignal):Promise<Page<ManagedUser>>;
  get(id:string,signal?:AbortSignal):Promise<ManagedUserDetail>;
  ledgers(id:string,query:LedgerQuery,signal?:AbortSignal):Promise<BalancePage>;
  entitlements(id:string,query:LedgerQuery,signal?:AbortSignal):Promise<EntitlementPage>;
};
const invalid = () => new Error('用户资料返回不完整，请重新查询');
const isObject = (v:unknown):v is Record<string,unknown> => typeof v==='object' && v!==null && !Array.isArray(v);
export const validUserID = (v:unknown):v is string => typeof v==='string' && /^[1-9][0-9]{0,18}$/.test(v) && BigInt(v)<=9223372036854775807n;
const count = (v:unknown):v is string => typeof v==='string' && /^(0|[1-9][0-9]{0,18})$/.test(v) && BigInt(v)<=9223372036854775807n;
const date = (v:unknown):v is string => typeof v==='string' && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/.test(v) && Number.isFinite(Date.parse(v));
const nullableDate = (v:unknown) => v===null || date(v);
const nullableID = (v:unknown) => v===null || validUserID(v);
export function moneyMicro(v:unknown,signed=false):bigint|null {
  if(typeof v!=='string' || !/^-?(0|[1-9][0-9]{0,12})\.[0-9]{6}$/.test(v))return null;
  const negative=v.startsWith('-'),unsigned=negative?v.slice(1):v;
  const [whole,fraction]=unsigned.split('.');
  const value=(BigInt(whole)*1000000n+BigInt(fraction))*(negative?-1n:1n);
  if((negative&&(!signed||value===0n)) || value < -9223372036854775808n || value > 9223372036854775807n)return null;
  return value;
}
const summaryKeys=['id','account_no','status','balance','currency','active_device_count','created_at','updated_at'];
function exactKeys(v:Record<string,unknown>,keys:string[]) { if(Object.keys(v).length!==keys.length || Object.keys(v).some(k=>!keys.includes(k)))throw invalid(); }
function userBase(v:unknown):ManagedUser {
  if(!isObject(v) || !validUserID(v.id) || typeof v.account_no!=='string' || !/^[1-9][0-9]{9}$/.test(v.account_no) ||
    typeof v.status!=='string' || !['ACTIVE','SUSPENDED','BANNED'].includes(v.status) || v.currency!=='USDT' ||
    (v.balance!==null&&moneyMicro(v.balance)===null) || !count(v.active_device_count) || !date(v.created_at) || !date(v.updated_at))throw invalid();
  return {id:v.id,account_no:v.account_no,status:v.status as UserStatus,balance:v.balance as string|null,currency:'USDT',active_device_count:v.active_device_count,created_at:v.created_at,updated_at:v.updated_at};
}
export function readManagedUser(v:unknown):ManagedUser {
  const value=userBase(v);exactKeys(v as Record<string,unknown>,summaryKeys);return value;
}
export function readUserDetail(v:unknown):ManagedUserDetail {
  const value=userBase(v);if(!isObject(v))throw invalid();
  exactKeys(v,[...summaryKeys,'invite_code','balance_updated_at','subscription']);
  if(typeof v.invite_code!=='string' || !v.invite_code || [...v.invite_code].length>12 || /\p{Cc}/u.test(v.invite_code) || !nullableDate(v.balance_updated_at) || (value.balance===null)!==(v.balance_updated_at===null))throw invalid();
  let subscription:SubscriptionRecord|null=null;
  if(v.subscription!==null){
    const s=v.subscription;
    if(!isObject(s) || !nullableID(s.plan_id) || !Number.isInteger(s.tier) || Number(s.tier)<0 || Number(s.tier)>9 ||
      !Number.isInteger(s.device_limit) || Number(s.device_limit)<0 || Number(s.device_limit)>2147483647 || !nullableDate(s.expires_at) || !nullableDate(s.period_reset_at) ||
      (s.period_quota_bytes!==null&&!count(s.period_quota_bytes)) || !count(s.period_used_bytes))throw invalid();
    exactKeys(s,['plan_id','tier','expires_at','device_limit','period_quota_bytes','period_used_bytes','period_reset_at']);
    subscription={plan_id:s.plan_id as string|null,tier:s.tier as number,expires_at:s.expires_at as string|null,device_limit:s.device_limit as number,period_quota_bytes:s.period_quota_bytes as string|null,period_used_bytes:s.period_used_bytes,period_reset_at:s.period_reset_at as string|null};
  }
  return {...value,invite_code:v.invite_code,balance_updated_at:v.balance_updated_at as string|null,subscription};
}
export function readBalanceEntry(v:unknown):BalanceEntry {
  if(!isObject(v) || !validUserID(v.id) || typeof v.entry_type!=='string' || !['DEPOSIT','PURCHASE','INVITE_REWARD','EXCEPTION_CREDIT','FULFILL_FALLBACK','ADMIN_CREDIT','ADMIN_DEBIT'].includes(v.entry_type) ||
    moneyMicro(v.amount,true)===null || moneyMicro(v.amount,true)===0n || moneyMicro(v.balance_after)===null ||
    typeof v.source_type!=='string' || !['CHAIN_TRANSFER','ORDER','INVITE_REWARD','DEPOSIT_EXCEPTION','ADMIN_APPROVAL'].includes(v.source_type) || !validUserID(v.source_id) ||
    typeof v.operator_type!=='string' || !['USER','ADMIN','SYSTEM'].includes(v.operator_type) || !date(v.created_at))throw invalid();
  exactKeys(v,['id','entry_type','amount','balance_after','source_type','source_id','operator_type','created_at']);
  return {id:v.id,entry_type:v.entry_type as BalanceEntryType,amount:v.amount as string,balance_after:v.balance_after as string,source_type:v.source_type as BalanceSourceType,source_id:v.source_id,operator_type:v.operator_type as BalanceEntry['operator_type'],created_at:v.created_at};
}
export function signedLedgerInteger(v:unknown):bigint|null {
  if(typeof v!=='string'||!/^(0|-?[1-9][0-9]{0,18})$/.test(v))return null;
  const value=BigInt(v);return value < -9223372036854775808n || value > 9223372036854775807n?null:value;
}
export function readEntitlementEntry(v:unknown):EntitlementEntry {
  const devices=isObject(v)?signedLedgerInteger(v.delta_devices):null;
  if(!isObject(v)||!validUserID(v.id)||typeof v.change_type!=='string'||!['PURCHASE','RENEW','TIER_CHANGE','TRAFFIC_PACK','DEVICE_ADDON','INVITE_REWARD','ADMIN_ADJUST','TRAFFIC_CONSUME_OVERFLOW','PERIOD_RESET','EXPIRE','REVOKE'].includes(v.change_type)||
    signedLedgerInteger(v.delta_seconds)===null||signedLedgerInteger(v.delta_bytes)===null||devices===null||devices < -2147483648n||devices > 2147483647n||
    typeof v.source_type!=='string'||!['ORDER','INVITE_REWARD','ADMIN_APPROVAL','SYSTEM'].includes(v.source_type)||!nullableID(v.source_id)||
    typeof v.operator_type!=='string'||!['USER','ADMIN','SYSTEM'].includes(v.operator_type)||!date(v.created_at))throw invalid();
  exactKeys(v,['id','change_type','delta_seconds','delta_bytes','delta_devices','source_type','source_id','operator_type','created_at']);
  return {id:v.id,change_type:v.change_type as EntitlementChangeType,delta_seconds:v.delta_seconds as string,delta_bytes:v.delta_bytes as string,delta_devices:v.delta_devices as string,source_type:v.source_type as EntitlementSourceType,source_id:v.source_id as string|null,operator_type:v.operator_type as EntitlementEntry['operator_type'],created_at:v.created_at};
}
export function userQueryProblem(q:UserQuery):string {
  if(!Number.isInteger(q.page)||q.page<1||q.page>10000||!Number.isInteger(q.page_size)||q.page_size<1||q.page_size>50)return '分页参数无效，请重新查询';
  if(q.account_no!==undefined&&!/^[1-9][0-9]{9}$/.test(q.account_no))return '请输入完整的10位账户号，首位不能为0';
  if(q.status!==undefined&&!['ACTIVE','SUSPENDED','BANNED'].includes(q.status))return '请选择有效的账户状态';
  return '';
}
function readPage<T extends {id:string}>(v:unknown,q:LedgerQuery,reader:(v:unknown)=>T):Page<T> {
  if(!isObject(v)||!Array.isArray(v.items)||!count(v.total)||v.page!==q.page||v.page_size!==q.page_size||v.items.length>q.page_size||BigInt(v.total)<BigInt(v.items.length))throw invalid();
  const items=v.items.map(reader);if(new Set(items.map(v=>v.id)).size!==items.length)throw invalid();
  return {items,total:v.total,page:q.page,page_size:q.page_size};
}
const queryString=(q:object)=>new URLSearchParams(Object.entries(q).filter(([,v])=>v!==undefined).map(([k,v])=>[k,String(v)])).toString();
export const liveUserSource:UserSource={
  list:async(q,signal)=>{
    const problem=userQueryProblem(q);if(problem)throw new Error(problem);
    const raw=await adminApi.get<unknown>(`/users?${queryString(q)}`,signal),value=readPage(raw,q,readManagedUser);
    exactKeys(raw as Record<string,unknown>,['items','total','page','page_size']);
    if(value.items.some(v=>(q.account_no&&v.account_no!==q.account_no)||(q.status&&v.status!==q.status)))throw invalid();
    return value;
  },
  get:async(id,signal)=>{
    if(!validUserID(id))throw new Error('用户编号无效');
    const value=readUserDetail(await adminApi.get(`/users/${id}`,signal));
    if(value.id!==id)throw invalid();return value;
  },
  entitlements:async(id,q,signal)=>{
    if(!validUserID(id))throw new Error('用户编号无效');const problem=userQueryProblem(q);if(problem)throw new Error(problem);
    const raw=await adminApi.get<unknown>(`/users/${id}/ledgers?${queryString({type:'entitlement',page:q.page,page_size:q.page_size})}`,signal);
    if(!isObject(raw)||raw.user_id!==id||raw.type!=='entitlement'||raw.currency!==null)throw invalid();
    exactKeys(raw,['user_id','type','currency','items','total','page','page_size']);
    return {...readPage(raw,q,readEntitlementEntry),user_id:id,type:'entitlement',currency:null};
  },
  ledgers:async(id,q,signal)=>{
    if(!validUserID(id))throw new Error('用户编号无效');const problem=userQueryProblem(q);if(problem)throw new Error(problem);
    const raw=await adminApi.get<unknown>(`/users/${id}/ledgers?${queryString({type:'balance',page:q.page,page_size:q.page_size})}`,signal);
    if(!isObject(raw)||raw.user_id!==id||raw.type!=='balance'||raw.currency!=='USDT')throw invalid();
    exactKeys(raw,['user_id','type','currency','items','total','page','page_size']);
    return {...readPage(raw,q,readBalanceEntry),user_id:id,type:'balance',currency:'USDT'};
  },
};
