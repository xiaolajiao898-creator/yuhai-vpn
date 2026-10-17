import {DeviceRevokeError,readDeviceDetail,readRevokeResult,revokeReasonProblem,type DeviceRevokeSource,type DeviceRevokeInput,type PreparedDeviceRevoke,type DeviceRevokeResult} from '../api/device-revoke.ts';
import type {UserDevice} from '../api/user-devices.ts';
export type DeviceTarget={userId:string;accountNo:string;device:UserDevice};
type Attempt={target:DeviceTarget;input:DeviceRevokeInput;prepared:PreparedDeviceRevoke};
export type DeviceRevokeState={scope:string;target:DeviceTarget;phase:'loading'|'form'|'preparing'|'confirm'|'submitting'|'recovery'|'checking'|'observed';reason:string;error?:string;attempt?:Attempt};
// Keep uncertain submissions across drawer/tab remounts. Reading ACTIVE cannot
// establish that an earlier request will not commit later; never offer resend.
const unresolved=new WeakMap<DeviceRevokeSource,Map<string,Attempt>>();
const targetKey=(target:DeviceTarget)=>`${target.userId}:${target.device.id}`;
const issue=(error:unknown)=>error instanceof Error?error.message:'暂时无法查询设备';
export function canRevokeDevice(device:UserDevice,permissions:readonly string[]){return device.status==='ACTIVE'&&permissions.includes('users.read')&&permissions.includes('devices.revoke');}
export function createDeviceRevokeFlow(source:DeviceRevokeSource,publish:(state:DeviceRevokeState|undefined)=>void,onVerified:(target:DeviceTarget,result?:DeviceRevokeResult)=>void){
  let scope:string|undefined,generation=0,state:DeviceRevokeState|undefined,controller=new AbortController();
  let pending=unresolved.get(source);if(!pending){pending=new Map();unresolved.set(source,pending);}
  const set=(next:DeviceRevokeState|undefined)=>{state=next;publish(next);};
  const cancel=()=>{generation++;controller.abort();controller=new AbortController();set(undefined);};
  const current=(token:number)=>token===generation&&scope!==undefined&&!controller.signal.aborted;
  function verified(target:DeviceTarget,result?:DeviceRevokeResult){set(undefined);onVerified(target,result);}
  return {
    context(next:string|undefined){if(scope!==next){cancel();scope=next;}},
    cancel,
    async open(target:DeviceTarget){
      if(scope===undefined||state?.phase==='submitting')return;cancel();const token=generation,localScope=scope;
      const frozen=structuredClone(target);set({scope:localScope,target:frozen,phase:'loading',reason:''});
      try{
        const detail=readDeviceDetail(await source.get(frozen.userId,frozen.device.id,controller.signal),frozen.userId,frozen.device.id);if(!current(token))return;
        const latest={...frozen,device:detail.device};const attempt=pending!.get(targetKey(frozen));
        if(latest.device.status==='REVOKED'){pending!.delete(targetKey(latest));set({scope:localScope,target:latest,phase:'observed',reason:'',error:'当前设备已撤销，无需再次操作。'});return;}
        set({scope:localScope,target:latest,phase:attempt?'recovery':'form',reason:attempt?.input.reason??'',attempt,error:attempt?'之前提交的结果尚未确认。只能查询状态，不能再次提交。':undefined});
      }catch(error){if(current(token))set({scope:localScope,target:frozen,phase:'recovery',reason:'',error:issue(error)});}
    },
    async prepare(reason:string){
      if(!state||state.phase!=='form'||scope===undefined)return;
      const problem=revokeReasonProblem(reason.trim());if(problem){set({...state,error:problem});return;}
      if(pending!.has(targetKey(state.target))){set({...state,phase:'recovery',error:'之前提交的结果尚未确认，请先查询状态。'});return;}
      const base=state,token=generation,input=Object.freeze({operation_id:crypto.randomUUID(),user_id:base.target.userId,reason:reason.trim()});
      set({...base,phase:'preparing',reason:input.reason,error:undefined});
      try{const prepared=await source.prepare(base.target.device.id,input);if(current(token))set({...base,phase:'confirm',reason:input.reason,error:undefined,attempt:{target:structuredClone(base.target),input,prepared}});}
      catch(error){if(current(token))set({...base,phase:'form',reason:input.reason,error:issue(error)});}
    },
    async confirm(code:string){
      if(!state||state.phase!=='confirm'||!state.attempt||scope===undefined)return;
      if(!/^\d{6}$/.test(code)){set({...state,error:'请输入6位动态验证码'});return;}
      const base=state,attempt=state.attempt,token=generation,key=targetKey(attempt.target);
      if(pending!.has(key)){set({...base,phase:'recovery',error:'该设备已有结果待确认的操作，请查询状态。'});return;}
      pending!.set(key,attempt);set({...base,phase:'submitting',error:undefined});
      try{
        const result=readRevokeResult(await attempt.prepared.confirm(code,controller.signal),attempt.target.device.id,attempt.input);
        if(pending!.get(key)===attempt)pending!.delete(key);
        if(current(token))verified({...attempt.target,device:result.device},result);
      }catch(error){
        const uncertain=!(error instanceof DeviceRevokeError)||error.uncertain;
        if(!uncertain&&pending!.get(key)===attempt)pending!.delete(key);
        if(current(token))set({...base,phase:uncertain?'recovery':'form',attempt:uncertain?attempt:undefined,error:uncertain?`${issue(error)}。请查询当前状态，页面不会重复提交。`:`${issue(error)}。撤销尚未提交，请核对后重新确认。`});
      }
    },
    async recover(){
      if(!state||state.phase!=='recovery'||scope===undefined)return;const base=state,token=generation;
      set({...base,phase:'checking',error:undefined});
      try{
        const detail=readDeviceDetail(await source.get(base.target.userId,base.target.device.id,controller.signal),base.target.userId,base.target.device.id);if(!current(token))return;
        const latest={...base.target,device:detail.device};
        if(latest.device.status==='REVOKED'){pending!.delete(targetKey(latest));verified(latest);}
        else if(!base.attempt&&!pending!.has(targetKey(latest)))set({...base,target:latest,phase:'form',error:undefined});
        else set({...base,target:latest,phase:'recovery',error:'当前仍显示已绑定，原请求结果尚未确认。不会再次提交；请稍后继续查询。'});
      }catch(error){if(current(token))set({...base,phase:'recovery',error:issue(error)});}
    },
  };
}
