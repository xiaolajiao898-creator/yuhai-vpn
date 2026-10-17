import type {AdminSession} from '../api/client';
import {userQueryProblem,validUserID,type BalanceEntry,type EntitlementEntry,type ManagedUserDetail,type UserSource,type UserStatus} from '../api/users.ts';

const decimal=(micro:bigint)=>`${micro<0n?'-':''}${(micro<0n?-micro:micro)/1000000n}.${String((micro<0n?-micro:micro)%1000000n).padStart(6,'0')}`;
const day=86400000;
export type PreviewUsers=UserSource&{setDeviceCount(id:string,count:string):void};
export function makePreviewUsers(session:AdminSession):PreviewUsers {
  const now=Date.now(),ledgerByUser=new Map<string,BalanceEntry[]>(),entitlementsByUser=new Map<string,EntitlementEntry[]>();
  const users:ManagedUserDetail[]=Array.from({length:26},(_,i)=>{
    const id=String(9007199254740993n+BigInt(i)),created=new Date(now-(90+i)*day).toISOString();
    const total=i===0?31:i===1||i===2?0:5;let balance=0n;
    const rows:BalanceEntry[]=Array.from({length:total},(_,j):BalanceEntry=>{
      const amount=j===0?1200000001n:j%2?-2500000n:25000000n;balance+=amount;
      return {id:String(9007199254800000n+BigInt(i*100+j)),entry_type:amount<0n?'PURCHASE':'DEPOSIT',amount:decimal(amount),balance_after:decimal(balance),source_type:amount<0n?'ORDER':'CHAIN_TRANSFER',source_id:String(81000+i*100+j),operator_type:amount<0n?'USER':'SYSTEM',created_at:new Date(now-(60-j)*day-i*3600000).toISOString()};
    }).reverse();
    ledgerByUser.set(id,rows);
    const examples:Pick<EntitlementEntry,'change_type'|'delta_seconds'|'delta_bytes'|'delta_devices'|'source_type'|'source_id'|'operator_type'>[]=[
      {change_type:'PURCHASE',delta_seconds:'2592000',delta_bytes:'0',delta_devices:'0',source_type:'ORDER',source_id:'82000',operator_type:'USER'},
      {change_type:'TRAFFIC_PACK',delta_seconds:'0',delta_bytes:'53687091200',delta_devices:'0',source_type:'ORDER',source_id:'82001',operator_type:'USER'},
      {change_type:'DEVICE_ADDON',delta_seconds:'0',delta_bytes:'0',delta_devices:'2',source_type:'ORDER',source_id:'82002',operator_type:'USER'},
      {change_type:'EXPIRE',delta_seconds:'-604800',delta_bytes:'0',delta_devices:'0',source_type:'SYSTEM',source_id:null,operator_type:'SYSTEM'},
      {change_type:'TRAFFIC_CONSUME_OVERFLOW',delta_seconds:'0',delta_bytes:'-268435456',delta_devices:'0',source_type:'SYSTEM',source_id:null,operator_type:'SYSTEM'},
      {change_type:'REVOKE',delta_seconds:'0',delta_bytes:'0',delta_devices:'-1',source_type:'SYSTEM',source_id:null,operator_type:'SYSTEM'},
      {change_type:'PERIOD_RESET',delta_seconds:'0',delta_bytes:'0',delta_devices:'0',source_type:'SYSTEM',source_id:null,operator_type:'SYSTEM'},
    ];
    const rights:EntitlementEntry[]=Array.from({length:i===0?27:i===1||i===2?0:3},(_,j)=>{
      const event=examples[j%examples.length];
      return {...event,source_id:event.source_id===null?null:String(82000+i*100+j),id:String(9007199254900000n+BigInt(i*100+j)),created_at:new Date(now-(35-j)*day-i*3600000).toISOString(),
        ...(i===0&&j===26?{change_type:'TRAFFIC_PACK' as const,delta_seconds:'0',delta_bytes:'9007199254740993',delta_devices:'0',source_type:'ORDER' as const,source_id:'82026',operator_type:'USER' as const}:{}),
        ...(i===0&&j===25?{change_type:'TRAFFIC_CONSUME_OVERFLOW' as const,delta_seconds:'0',delta_bytes:'-9007199254740993',delta_devices:'0',source_type:'SYSTEM' as const,source_id:null,operator_type:'SYSTEM' as const}:{}),
      };
    }).reverse();
    entitlementsByUser.set(id,rights);
    const status:UserStatus=i%11===1?'SUSPENDED':i%13===3?'BANNED':'ACTIVE';
    const alphabet='23456789ABCDEFGHJKMNPQRSTUVWXYZ';
    return {id,account_no:String(8600888000+i),status,balance:i===1?null:decimal(balance),currency:'USDT',active_device_count:String(i===2?0:1+i%3),created_at:created,updated_at:new Date(now-(i+1)*3600000).toISOString(),invite_code:`YH23${alphabet[Math.floor(i/alphabet.length)]}${alphabet[i%alphabet.length]}`,balance_updated_at:i===1?null:rows[0]?.created_at??created,
      subscription:i===1||i===2?null:{plan_id:'11',tier:2,expires_at:new Date(now+(status==='ACTIVE'?30:-7)*day).toISOString(),device_limit:3,period_quota_bytes:i%5===4?null:'107374182400',period_used_bytes:'18790481920',period_reset_at:new Date(now+9*day).toISOString()}};
  });
  const check=(signal:AbortSignal|undefined,ledger=false)=>{
    if(signal?.aborted)throw new DOMException('请求已取消','AbortError');
    if(!session.permissions.includes('users.read')||(ledger&&!session.permissions.includes('users.ledgers.read')))throw Object.assign(new Error('当前演示账号没有查看权限'),{status:403});
  };
  const find=(id:string)=>{if(!validUserID(id))throw Object.assign(new Error('用户编号无效'),{status:400});const user=users.find(u=>u.id===id);if(!user)throw Object.assign(new Error('用户不存在'),{status:404});return user;};
  return {
    setDeviceCount:(id,count)=>{if(!/^(0|[1-9][0-9]*)$/.test(count)||BigInt(count)>50n)throw new Error('演示设备数量无效');find(id).active_device_count=count;},
    list:async(query,signal)=>{
      check(signal);const problem=userQueryProblem(query);if(problem)throw new Error(problem);
      const filtered=users.filter(u=>(!query.account_no||u.account_no===query.account_no)&&(!query.status||u.status===query.status));
      const items=filtered.slice((query.page-1)*query.page_size,query.page*query.page_size).map(({invite_code:_invite,balance_updated_at:_balanceAt,subscription:_subscription,...summary})=>summary);
      return structuredClone({items,total:String(filtered.length),page:query.page,page_size:query.page_size});
    },
    get:async(id,signal)=>{check(signal);return structuredClone(find(id));},
    entitlements:async(id,query,signal)=>{
      check(signal,true);const problem=userQueryProblem(query);if(problem)throw new Error(problem);find(id);
      const all=entitlementsByUser.get(id)!;
      return structuredClone({user_id:id,type:'entitlement',currency:null,items:all.slice((query.page-1)*query.page_size,query.page*query.page_size),total:String(all.length),page:query.page,page_size:query.page_size});
    },
    ledgers:async(id,query,signal)=>{
      check(signal,true);const problem=userQueryProblem(query);if(problem)throw new Error(problem);find(id);
      const all=ledgerByUser.get(id)!;
      return structuredClone({user_id:id,type:'balance',currency:'USDT',items:all.slice((query.page-1)*query.page_size,query.page*query.page_size),total:String(all.length),page:query.page,page_size:query.page_size});
    },
  };
}
