import type {AdminSession} from '../api/client.ts';
import type {PreviewUsers} from './users.ts';
import {deviceQueryProblem,readUserDevicePage,type UserDevice,type UserDeviceSource} from '../api/user-devices.ts';
import {DeviceRevokeError,readDeviceDetail,readRevokeInput,readRevokeResult,type DeviceRevokeSource,type DeviceRevokeResult} from '../api/device-revoke.ts';

export function makePreviewUserDevices(session:AdminSession,users:PreviewUsers,onAudit?:(deviceId:string)=>void):UserDeviceSource&{revocations:DeviceRevokeSource} {
  const now=Date.now(),day=86400000,byUser=new Map<string,UserDevice[]>(),operations=new Map<string,{body:string;result:DeviceRevokeResult}>();
  const check=(signal?:AbortSignal,write=false)=>{
    signal?.throwIfAborted();
    if(!session.permissions.includes('users.read')||(write&&!session.permissions.includes('devices.revoke')))throw Object.assign(new Error('当前演示账号没有此项设备操作权限'),{status:403});
  };
  async function rowsFor(id:string,signal?:AbortSignal){
    check(signal);const user=await users.get(id,signal);check(signal);
    let rows=byUser.get(id);if(rows)return rows;
    const active=Number(user.active_device_count),first=user.account_no==='8600888000';
    if(!Number.isSafeInteger(active)||active<0||active>50)throw new Error('演示设备数量无效');
    rows=Array.from({length:active+(first?22:active?2:0)},(_,index):UserDevice=>{
      const revoked=index>=active;
      return {id:String(9007199254900000n+(BigInt(user.account_no)-8600888000n)*100n+BigInt(index)),platform:index%2?'ANDROID':'IOS',display_name:index%7===1?null:index===0?'我的手机':index%2?'备用 Android':'个人 iPhone',status:revoked?'REVOKED':'ACTIVE',created_at:new Date(now-(7+index)*day).toISOString(),last_seen_at:index%7===0?null:new Date(now-(revoked?3:1)*day).toISOString(),revoked_at:revoked?new Date(now-2*day).toISOString():null};
    });byUser.set(id,rows);return rows;
  }
  const revocations:DeviceRevokeSource={
    get:async(userId,deviceId,signal)=>{
      const device=(await rowsFor(userId,signal)).find(d=>d.id===deviceId);if(!device)throw Object.assign(new Error('该账户下不存在此设备'),{status:404});
      return readDeviceDetail(structuredClone({user_id:userId,device}),userId,deviceId);
    },
    prepare:async(deviceId,input)=>{
      check(undefined,true);const frozen=Object.freeze(readRevokeInput(input,deviceId));let attempted=false;
      return Object.freeze({confirm:async(code:string,signal:AbortSignal)=>{
        if(attempted)throw new DeviceRevokeError(new Error('本次确认已使用，请查询状态'),false);attempted=true;
        try {
          check(signal,true);if(code!=='123456')throw new Error('演示验证码为123456');
          const rows=await rowsFor(frozen.user_id,signal);check(signal,true);
          const body=JSON.stringify({deviceId,...frozen}),prior=operations.get(frozen.operation_id);
          if(prior){if(prior.body!==body)throw new Error('操作编号与先前内容不一致');return structuredClone(prior.result);}
          const device=rows.find(d=>d.id===deviceId);if(!device)throw Object.assign(new Error('该账户下不存在此设备'),{status:404});
          const already=device.status==='REVOKED';if(!already){device.status='REVOKED';device.revoked_at=new Date().toISOString();}
          users.setDeviceCount(frozen.user_id,String(rows.filter(d=>d.status==='ACTIVE').length));
          const result=readRevokeResult({operation_id:frozen.operation_id,user_id:frozen.user_id,device:structuredClone(device),already_revoked:already,node_sync:already?'NOT_REQUIRED':'QUEUED'},deviceId,frozen);
          operations.set(frozen.operation_id,{body,result:structuredClone(result)});onAudit?.(deviceId);return result;
        }catch(error){throw new DeviceRevokeError(error,false);}
      }});
    },
  };
  return {revocations,list:async(id,query,signal)=>{
    check(signal);const problem=deviceQueryProblem(query);if(problem)throw new Error(problem);
    const rows=(await rowsFor(id,signal)).filter(device=>!query.status||device.status===query.status);
    return readUserDevicePage(structuredClone({user_id:id,items:rows.slice((query.page-1)*query.page_size,query.page*query.page_size),total:String(rows.length),page:query.page,page_size:query.page_size}),id,query);
  }};
}
