import test from 'node:test';
import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import {createLiveDeviceRevokeSource,DeviceRevokeError,readRevokeInput,readDeviceDetail,readRevokeResult,revokeReasonProblem} from '../src/api/device-revoke.ts';
import {createDeviceRevokeFlow,canRevokeDevice} from '../src/pages/device-revoke-model.ts';
import {makePreviewUsers} from '../src/preview/users.ts';
import {makePreviewUserDevices} from '../src/preview/user-devices.ts';
import {makePreviewSource} from '../src/preview/source.ts';
const userId='9007199254740993',deviceId='9007199254900000';
const stamp='2026-09-12T00:00:00Z';
const input=()=>({operation_id:'cafeba11-0000-4000-8000-000000000001',user_id:userId,reason:'用户反馈设备遗失'});
const device=(status='ACTIVE')=>({id:deviceId,platform:'IOS',display_name:'我的手机',status,created_at:stamp,last_seen_at:null,revoked_at:status==='REVOKED'?stamp:null});
const detail=(status='ACTIVE')=>({user_id:userId,device:device(status)});
const result=(body=input())=>({...detail('REVOKED'),operation_id:body.operation_id,already_revoked:false,node_sync:'QUEUED'});
const target=()=>({userId,accountNo:'8600888000',device:device()});
const signal=()=>new AbortController().signal;
const response=value=>new Response(JSON.stringify(value));
const token=()=>({step_up_token:'T'.repeat(43),expires_at:new Date(Date.now()+60000).toISOString()});
const deferred=()=>{let resolve,reject;const promise=new Promise((yes,no)=>{resolve=yes;reject=no;});return {promise,resolve,reject};};
function fetchMock(t,fn){const original=globalThis.fetch;globalThis.fetch=fn;t.after(()=>globalThis.fetch=original);}
function fixture(overrides={}){
 let current,verified=0,prepares=0,confirms=0,lastInput;
 const source={get:async()=>detail(),prepare:async(_id,body)=>{prepares++;lastInput=body;return {confirm:async()=>{confirms++;return result(body);}};},...overrides};
 const flow=createDeviceRevokeFlow(source,s=>current=s,()=>verified++);flow.context('account-scope');
 return {source,flow,get current(){return current},get verified(){return verified},get prepares(){return prepares},get confirms(){return confirms},get lastInput(){return lastInput}};
}
async function ready(s){await s.flow.open(target());assert.equal(s.current.phase,'form');await s.flow.prepare('  用户反馈设备遗失  ');assert.equal(s.current.phase,'confirm');}

test('revoke contract preserves exact targets and rejects malformed payloads or contradictory success',()=>{
 assert.equal(readRevokeInput(input(),deviceId).user_id,userId);assert.equal(readDeviceDetail(detail(),userId,deviceId).device.id,deviceId);
 for(const body of [{...input(),user_id:9},{...input(),operation_id:'bad'},{...input(),reason:'x\ny'},{...input(),reason:' '},{...input(),reason:'好'.repeat(501)},{...input(),secret:'hidden'}])assert.throws(()=>readRevokeInput(body,deviceId));
 assert.equal(revokeReasonProblem('好'.repeat(500)),'');
 for(const raw of [{}, {...detail(),user_id:'42'}, {...detail(),device:{...device(),id:'42'}},{...detail(),session:'private'}])assert.throws(()=>readDeviceDetail(raw,userId,deviceId));
 assert.equal(readRevokeResult(result(),deviceId,input()).device.status,'REVOKED');
 assert.equal(readRevokeResult({...result(),already_revoked:true,node_sync:'QUEUED',device:{...device('REVOKED'),revoked_at:null}},deviceId,input()).already_revoked,true);
 for(const raw of [{}, {...result(),operation_id:'other'}, {...result(),user_id:'42'}, {...result(),device:device()}, {...result(),node_sync:'DISCONNECTED'}, {...result(),node_sync:['QUEUED']}, {...result(),device:{...device('REVOKED'),revoked_at:null}},{...result(),token:'hidden'}])assert.throws(()=>readRevokeResult(raw,deviceId,input()));
});
test('revoke freezes exact DELETE URI/body bytes and binds them to devices.revoke step-up',async t=>{
 const requests=[];fetchMock(t,async(uri,options)=>{requests.push({uri,...options});return response(uri.endsWith('/step-up')?token():result());});
 const body=input(),source=createLiveDeviceRevokeSource();const prepared=await source.prepare(deviceId,body);body.user_id='42';body.reason='changed';
 await prepared.confirm('123456',signal());assert.equal(requests.length,2);
 assert.equal(requests[1].uri,`/admin/v1/devices/${deviceId}`);assert.equal(requests[1].method,'DELETE');assert.equal(requests[1].body,JSON.stringify(input()));
 const stepup=JSON.parse(requests[0].body);assert.equal(stepup.action_code,'devices.revoke');
 assert.equal(stepup.payload_hash,createHash('sha256').update(`v1\nDELETE\n/admin/v1/devices/${deviceId}\n${requests[1].body}`).digest('hex'));
 assert.equal(requests[1].headers['X-Step-Up-Token'],'T'.repeat(43));assert.equal(requests[1].credentials,'same-origin');
 await assert.rejects(prepared.confirm('654321',signal()));assert.equal(requests.length,2);
});
test('step-up failures and cancellation before DELETE never send a business write',async t=>{
 let calls=0;const gate=deferred();fetchMock(t,async()=>{calls++;return gate.promise;});
 const controller=new AbortController(),source=createLiveDeviceRevokeSource();const prepared=await source.prepare(deviceId,input());
 const pending=prepared.confirm('123456',controller.signal);controller.abort();await assert.rejects(pending,e=>e instanceof DeviceRevokeError&&!e.uncertain);
 gate.resolve(response(token()));await Promise.resolve();assert.equal(calls,1);
});
test('a denied step-up can be re-prepared but not confirmed twice',async t=>{
 let calls=0;fetchMock(t,async()=>{calls++;return new Response(JSON.stringify({error:{message:'无撤销权限'}}),{status:403});});
 const source=createLiveDeviceRevokeSource(),prepared=await source.prepare(deviceId,input());await assert.rejects(prepared.confirm('123456',signal()),e=>!e.uncertain);
 await assert.rejects(prepared.confirm('123456',signal()));assert.equal(calls,1);
});
test('timeout or malformed successful DELETE remains uncertain and never retries',async t=>{
 for(const mode of ['timeout','empty','http']){
  let deletes=0;fetchMock(t,async(uri)=>{if(uri.endsWith('/step-up'))return response(token());deletes++;if(mode==='timeout')return new Promise(()=>{});return mode==='empty'?response({}):new Response('{}',{status:503});});
  const prepared=await createLiveDeviceRevokeSource(5).prepare(deviceId,input());await assert.rejects(prepared.confirm('123456',signal()),e=>e.uncertain===true);
  await assert.rejects(prepared.confirm('123456',signal()));assert.equal(deletes,1);
 }
});
test('exact device reread is GET-only, target-bound, bounded and cancellation-aware',async t=>{
 const calls=[];fetchMock(t,async(uri,options)=>{calls.push({uri,...options});return response(detail());});
 const source=createLiveDeviceRevokeSource();assert.equal((await source.get(userId,deviceId,signal())).device.id,deviceId);
 assert.equal(calls[0].uri,`/admin/v1/users/${userId}/devices/${deviceId}`);assert.equal(calls[0].method,'GET');assert.equal(calls[0].body,undefined);
 assert.ok(!('X-Step-Up-Token' in calls[0].headers));
 let stopped;fetchMock(t,async(_uri,options)=>{stopped=options.signal;return new Promise(()=>{});});await assert.rejects(createLiveDeviceRevokeSource(5).get(userId,deviceId,signal()),/超时/);assert.equal(stopped.aborted,true);
});
test('UI flow locks preparation and double confirm, keeps the reviewed reason and target',async()=>{
 const s=fixture();await s.flow.open(target());await Promise.all([s.flow.prepare('  用户反馈设备遗失  '),s.flow.prepare('different')]);assert.equal(s.prepares,1);assert.equal(s.current.reason,input().reason);
 await Promise.all([s.flow.confirm('123456'),s.flow.confirm('123456')]);assert.equal(s.confirms,1);assert.equal(s.verified,1);assert.equal(s.current,undefined);assert.equal(s.lastInput.user_id,userId);
});
test('late target reads cannot open a confirmation for the previous account',async()=>{
 const old=deferred();const other={userId:'9007199254740994',accountNo:'8600888001',device:{...device(),id:'9007199254900100'}};
 const s=fixture({get:async(id)=>id===userId?old.promise:{user_id:other.userId,device:other.device}});
 const first=s.flow.open(target());s.flow.context('other-user');await s.flow.open(other);old.resolve(detail());await first;assert.equal(s.current.target.userId,other.userId);assert.equal(s.current.phase,'form');
});
test('permission/session change cancels pending step-up and prevents DELETE',async t=>{
 const gate=deferred();let deletes=0;fetchMock(t,async(uri)=>uri.endsWith('/step-up')?gate.promise:(deletes++,response(result())));
 const live=createLiveDeviceRevokeSource(),s=fixture({get:async()=>detail(),prepare:live.prepare});await ready(s);const pending=s.flow.confirm('123456');s.flow.context(undefined);gate.resolve(response(token()));await pending;
 assert.equal(deletes,0);assert.equal(s.current,undefined);assert.equal(s.verified,0);assert.equal(canRevokeDevice(device(),['users.read']),false);assert.equal(canRevokeDevice(device('REVOKED'),['users.read','devices.revoke']),false);
});
test('unknown result survives drawer remount; ACTIVE reread does not permit resending',async()=>{
 let prepared=0;const source={get:async()=>detail(),prepare:async()=>{prepared++;return {confirm:async()=>{throw new DeviceRevokeError(new Error('timeout'),true)}}}};
 const s=fixture(source);await ready(s);await s.flow.confirm('123456');assert.equal(s.current.phase,'recovery');s.flow.cancel();
 let state;const next=createDeviceRevokeFlow(s.source,s=>state=s,()=>assert.fail('not verified'));next.context('new-drawer');await next.open(target());assert.equal(state.phase,'recovery');await next.prepare('不能重发');await next.recover();assert.equal(state.phase,'recovery');assert.match(state.error,/仍显示已绑定/);assert.equal(prepared,1);
});
test('REVOKED reread confirms current state, not the original request outcome',async()=>{
 let status='ACTIVE',observed;const s=fixture({get:async()=>detail(status),prepare:async()=>({confirm:async()=>{throw new DeviceRevokeError(new Error('timeout'),true)}})});
 const flow=createDeviceRevokeFlow(s.source,()=>{},(_target,result)=>observed={result});flow.context('scope');await flow.open(target());await flow.prepare('设备遗失');await flow.confirm('123456');status='REVOKED';await flow.recover();assert.deepEqual(observed,{result:undefined});
});
test('404 or 403 during recovery is not a successful device revocation',async()=>{
 let denied=false;const s=fixture({get:async()=>{if(denied)throw Object.assign(new Error('无权限'),{status:403});return detail();},prepare:async()=>({confirm:async()=>{throw new DeviceRevokeError(new Error('timeout'),true)}})});
 await ready(s);await s.flow.confirm('123456');denied=true;await s.flow.recover();assert.equal(s.current.phase,'recovery');assert.equal(s.verified,0);
});
test('late write success after context change never updates another account',async()=>{
 const gate=deferred();let body;const s=fixture({prepare:async(_id,input)=>{body=input;return {confirm:async()=>gate.promise}}});await ready(s);const pending=s.flow.confirm('123456');s.flow.context('other-account');gate.resolve(result(body));await pending;assert.equal(s.verified,0);assert.equal(s.current,undefined);
});
test('preview revocation persists, preserves history and pagination, updates counts and logs once',async t=>{
 fetchMock(t,async()=>assert.fail('preview cannot access network'));
 const session={id:'18',username:'演示',permissions:['users.read','devices.revoke','audit.read'],csrf_token:''};
 const users=makePreviewUsers(session),audit=makePreviewSource(session),source=makePreviewUserDevices(session,users,audit.recordDeviceRevocation),q={page:1,page_size:20};
 const first=await source.list(userId,q),before=(await audit.listAudit({page:1,page_size:50})).items.length;
 const prepared=await source.revocations.prepare(deviceId,input()),revoked=await prepared.confirm('123456',signal());assert.equal(revoked.device.status,'REVOKED');
 await assert.rejects(prepared.confirm('123456',signal()));assert.equal((await users.get(userId)).active_device_count,'0');
 assert.equal((await source.list(userId,{...q,status:'ACTIVE'})).total,'0');assert.equal((await source.list(userId,{...q,status:'REVOKED'})).total,'23');
 assert.equal((await source.list(userId,q)).items.length,20);assert.equal((await source.list(userId,{page:2,page_size:20})).items.length,3);assert.equal((await source.list(userId,q)).items[0].id,first.items[0].id);
 const replay=await source.revocations.prepare(deviceId,input());await replay.confirm('123456',signal());const logs=(await audit.listAudit({page:1,page_size:50})).items;assert.equal(logs.length,before+1);assert.equal(logs[0].action_code,'devices.revoke');assert.equal(logs[0].target_id,deviceId);
 await assert.rejects(source.revocations.get('9007199254740994',deviceId,signal()),e=>e.status===404);
});
test('preview enforces revoke permission at confirmation and does not revive records on reads',async()=>{
 const session={id:'18',username:'演示',permissions:['users.read','devices.revoke'],csrf_token:''};const users=makePreviewUsers(session),source=makePreviewUserDevices(session,users);
 const prepared=await source.revocations.prepare(deviceId,input());session.permissions=['users.read'];await assert.rejects(prepared.confirm('123456',signal()),e=>e.uncertain===false);assert.equal((await source.revocations.get(userId,deviceId,signal())).device.status,'ACTIVE');
 session.permissions.push('devices.revoke');const p=await source.revocations.prepare(deviceId,input());await p.confirm('123456',signal());const historical=await source.revocations.get(userId,deviceId,signal());historical.device.status='ACTIVE';assert.equal((await source.revocations.get(userId,deviceId,signal())).device.status,'REVOKED');
});
