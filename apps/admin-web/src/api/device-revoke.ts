import {adminApi,prepareProtectedRequest} from './client.ts';
import {validUserID} from './users.ts';
import {readUserDevice,type UserDevice} from './user-devices.ts';

export type DeviceDetail={user_id:string;device:UserDevice};
export type DeviceRevokeInput={operation_id:string;user_id:string;reason:string};
export type DeviceRevokeResult=DeviceDetail&{operation_id:string;already_revoked:boolean;node_sync:'QUEUED'|'NOT_REQUIRED'};
export type PreparedDeviceRevoke={confirm(code:string,signal:AbortSignal):Promise<DeviceRevokeResult>};
export type DeviceRevokeSource={
  get(userId:string,deviceId:string,signal:AbortSignal):Promise<DeviceDetail>;
  prepare(deviceId:string,input:DeviceRevokeInput):Promise<PreparedDeviceRevoke>;
};
export class DeviceRevokeError extends Error {
  readonly uncertain:boolean;
  constructor(error:unknown,uncertain:boolean){super(error instanceof Error?error.message:'撤销结果未能确认');this.name='DeviceRevokeError';this.uncertain=uncertain;}
}
const invalid=()=>new Error('设备操作返回不完整，请查询当前状态');
const record=(v:unknown):v is Record<string,unknown>=>!!v&&typeof v==='object'&&!Array.isArray(v);
const exact=(v:Record<string,unknown>,keys:string[])=>Object.keys(v).length===keys.length&&Object.keys(v).every(k=>keys.includes(k));
export function revokeReasonProblem(reason:string):string {
  return typeof reason!=='string'||reason!==reason.trim()||![...reason].length||[...reason].length>500||/\p{Cc}/u.test(reason)?'请填写1至500字的撤销原因，不含换行或控制字符':'';
}
export function readRevokeInput(value:DeviceRevokeInput,deviceId:string):DeviceRevokeInput {
  if(!validUserID(deviceId)||!record(value)||!exact(value,['operation_id','user_id','reason'])||!validUserID(value.user_id)||typeof value.operation_id!=='string'||! /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(value.operation_id)||typeof value.reason!=='string'||revokeReasonProblem(value.reason))throw new Error('撤销资料无效，请重新核对目标和原因');
  return {operation_id:value.operation_id,user_id:value.user_id,reason:value.reason};
}
export function readDeviceDetail(raw:unknown,userId:string,deviceId:string):DeviceDetail {
  if(!validUserID(userId)||!validUserID(deviceId)||!record(raw)||!exact(raw,['user_id','device'])||raw.user_id!==userId)throw invalid();
  const device=readUserDevice(raw.device);if(device.id!==deviceId)throw invalid();return {user_id:userId,device};
}
export function readRevokeResult(raw:unknown,deviceId:string,input:DeviceRevokeInput):DeviceRevokeResult {
  if(!record(raw)||!exact(raw,['operation_id','user_id','device','already_revoked','node_sync'])||raw.operation_id!==input.operation_id||typeof raw.already_revoked!=='boolean'||typeof raw.node_sync!=='string'||!['QUEUED','NOT_REQUIRED'].includes(raw.node_sync))throw invalid();
  const detail=readDeviceDetail({user_id:raw.user_id,device:raw.device},input.user_id,deviceId);
  if(detail.device.status!=='REVOKED'||(!raw.already_revoked&&detail.device.revoked_at===null))throw invalid();
  return {...detail,operation_id:input.operation_id,already_revoked:raw.already_revoked,node_sync:raw.node_sync as DeviceRevokeResult['node_sync']};
}
async function bounded<T>(read:Promise<T>,signal:AbortSignal,timeout:number):Promise<T> {
  let timer:ReturnType<typeof setTimeout>|undefined;let cancel:()=>void=()=>{};
  const stopped=new Promise<never>((_,reject)=>{cancel=()=>reject(new DOMException('操作已取消','AbortError'));if(signal.aborted){cancel();return;}signal.addEventListener('abort',cancel,{once:true});timer=setTimeout(()=>reject(new Error('请求超时，请查询当前状态')),timeout);});
  try{return await Promise.race([read,stopped]);}finally{if(timer!==undefined)clearTimeout(timer);signal.removeEventListener('abort',cancel);}
}
export function createLiveDeviceRevokeSource(timeout=12000):DeviceRevokeSource {
  return {
    get:async(userId,deviceId,signal)=>{
      if(!validUserID(userId)||!validUserID(deviceId))throw new Error('用户或设备编号无效');signal.throwIfAborted();
      const controller=new AbortController(),cancel=()=>controller.abort();signal.addEventListener('abort',cancel,{once:true});
      try{const raw=await bounded(adminApi.get(`/users/${userId}/devices/${deviceId}`,controller.signal),signal,timeout);signal.throwIfAborted();return readDeviceDetail(raw,userId,deviceId);}finally{controller.abort();signal.removeEventListener('abort',cancel);}
    },
    prepare:async(deviceId,input)=>{
      const frozen=Object.freeze(readRevokeInput(input,deviceId));
      const prepared=await prepareProtectedRequest<unknown>('DELETE',`/devices/${deviceId}`,frozen);
      let attempted=false,sent=false;
      return Object.freeze({confirm:async(code:string,signal:AbortSignal)=>{
        if(attempted)throw new DeviceRevokeError(new Error('本次确认已使用，请查询当前状态'),sent);attempted=true;
        try {
          signal.throwIfAborted();if(!/^\d{6}$/.test(code))throw new Error('请输入6位动态验证码');
          const token=await bounded(adminApi.stepUp({action_code:'devices.revoke',payload_hash:prepared.payloadHash,totp_code:code}),signal,timeout);
          signal.throwIfAborted();
          if(!token||typeof token.step_up_token!=='string'||! /^[A-Za-z0-9_-]{43}$/.test(token.step_up_token)||typeof token.expires_at!=='string'||!Number.isFinite(Date.parse(token.expires_at))||Date.parse(token.expires_at)<=Date.now())throw invalid();
          sent=true;
          // Cancellation stops waiting; it cannot undo a write already sent.
          return readRevokeResult(await bounded(prepared.send(token.step_up_token),signal,timeout),deviceId,frozen);
        }catch(error){throw new DeviceRevokeError(error,sent);}
      }});
    },
  };
}
export const liveDeviceRevokeSource=createLiveDeviceRevokeSource();
