import test from 'node:test';
import assert from 'node:assert/strict';
import {deviceQueryProblem,readUserDevice,readUserDevicePage,liveUserDeviceSource,loadUserDevices} from '../src/api/user-devices.ts';
import {deviceName,deviceSeen,deviceRevoked,deviceStatusLabels} from '../src/pages/user-devices-model.ts';
import {makePreviewUserDevices} from '../src/preview/user-devices.ts';
import {makePreviewUsers} from '../src/preview/users.ts';
const id='9007199254740993',query={page:1,page_size:20};
const stamp='2026-09-12T00:00:00Z';
const device=(n=0)=>({id:String(9007199254900000n+BigInt(n)),platform:'IOS',display_name:null,status:'ACTIVE',created_at:stamp,last_seen_at:null,revoked_at:null});
const page=(items=[device()],total=String(items.length),q=query)=>({user_id:id,items,total,...q});
const session=()=>({id:'18',username:'tester',permissions:['users.read'],csrf_token:''});
function fetchMock(t,fn){const original=globalThis.fetch;globalThis.fetch=fn;t.after(()=>globalThis.fetch=original);}
const response=value=>new Response(JSON.stringify(value));

test('device DTO preserves big IDs and absent names/times while rejecting sensitive or contradictory fields',()=>{
  assert.equal(readUserDevice(device()).id,'9007199254900000');
  assert.equal(deviceName(device()),'未命名设备');assert.equal(deviceSeen(device()),'暂无活跃记录');assert.equal(deviceStatusLabels.ACTIVE,'已绑定');
  assert.equal(deviceRevoked(device()),'未撤销');assert.equal(deviceRevoked(readUserDevice({...device(),status:'REVOKED'})),'暂无撤销时间');
  for(const change of [{id:9007199254900000},{id:'9223372036854775808'},{platform:['IOS']},{platform:'LINUX'},{status:'ONLINE'},{display_name:'x\n'},{display_name:'好'.repeat(65)},{display_name:undefined},{created_at:'2026-02-30T00:00:00Z'},{created_at:'2026-09-12T25:00:00Z'},{last_seen_at:undefined},{revoked_at:stamp},{device_uid:'hidden'},{pubkey:'hidden'},{fingerprint_hash:'hidden'}])assert.throws(()=>readUserDevice({...device(),...change}));
  assert.equal(readUserDevice({...device(),display_name:'好'.repeat(64)}).display_name.length,64);
  assert.equal(readUserDevice({...device(),created_at:'2024-02-29T12:00:00.123456789+08:00'}).created_at,'2024-02-29T12:00:00.123456789+08:00');
});
test('device page validates target, exact pagination counts, duplicates, status filters and response whitelist',()=>{
  assert.equal(readUserDevicePage(page(),id,query).total,'1');
  for(const change of [{user_id:'42'},{page:2},{page_size:10},{total:'2'},{total:1},{total:'01'},{items:[device(),device()],total:'2'},{items:[{...device(),status:'REVOKED'}]},{token:'hidden'}])assert.throws(()=>readUserDevicePage({...page(),...change},id,{...query,status:'ACTIVE'}));
  assert.equal(readUserDevicePage(page([],'21',{page:3,page_size:20}),id,{page:3,page_size:20}).items.length,0);
  assert.throws(()=>readUserDevicePage(page([],'21',{page:2,page_size:20}),id,{page:2,page_size:20}));
});
test('device source uses GET only and forwards exact IDs, filters and cancellation without write headers',async t=>{
  let request;const controller=new AbortController();
  fetchMock(t,async(url,options)=>{request={url,...options};return response(page());});
  assert.equal((await liveUserDeviceSource.list(id,{...query,status:'ACTIVE'},controller.signal)).items.length,1);
  assert.equal(request.url,`/admin/v1/users/${id}/devices?page=1&page_size=20&status=ACTIVE`);
  assert.equal(request.method,'GET');assert.equal(request.signal,controller.signal);assert.equal(request.body,undefined);
  assert.ok(!('X-Step-Up-Token' in request.headers)&&!('X-CSRF-Token' in request.headers));
});
test('device invalid input sends no request and failed/malformed responses never become empty success',async t=>{
  let calls=0,value={};fetchMock(t,async()=>{calls++;return value instanceof Response?value:response(value);});
  for(const q of [{page:0,page_size:20},{page:10001,page_size:20},{page:1,page_size:51},{page:1.5,page_size:20},{...query,status:'ONLINE'},{...query,user_id:'42'}]){assert.ok(deviceQueryProblem(q));await assert.rejects(liveUserDeviceSource.list(id,q));}
  await assert.rejects(liveUserDeviceSource.list('../7',query));assert.equal(calls,0);
  await assert.rejects(liveUserDeviceSource.list(id,query));
  value=new Response('<html>gateway</html>');await assert.rejects(liveUserDeviceSource.list(id,query));
  value=new Response(JSON.stringify({error:{message:'forbidden'}}),{status:403});await assert.rejects(liveUserDeviceSource.list(id,query),e=>e.status===403);
  value=page([],'0');assert.equal((await liveUserDeviceSource.list(id,query)).items.length,0);
});
test('bounded device read aborts one timed-out transport without retry and rejects a late response',async()=>{
  let signal,resolve,calls=0;const parent=new AbortController();
  const source={list:(_id,_q,s)=>{calls++;signal=s;return new Promise(r=>resolve=r);}};
  await assert.rejects(loadUserDevices(source,id,query,parent.signal,5),/超时/);
  assert.equal(calls,1);assert.equal(signal.aborted,true);resolve(page());
  assert.equal((await loadUserDevices({list:async()=>page()},id,query,parent.signal,1000)).total,'1');
});
test('bounded device read propagates parent cancellation even if transport ignores it',async()=>{
  const parent=new AbortController();let signal;const source={list:(_id,_q,s)=>{signal=s;return new Promise(()=>{});}};
  const pending=loadUserDevices(source,id,query,parent.signal,1000);await Promise.resolve();parent.abort();
  await assert.rejects(pending,{name:'AbortError'});assert.equal(signal.aborted,true);
  let calls=0;await assert.rejects(loadUserDevices({list:async()=>{calls++;return page();}},id,query,parent.signal),{name:'AbortError'});assert.equal(calls,0);
});
test('offline device preview keeps counts aligned with users, supports 20 plus 3 pages and isolates accounts',async t=>{
  fetchMock(t,async()=>assert.fail('preview cannot call real API'));const owner=session(),users=makePreviewUsers(owner),source=makePreviewUserDevices(owner,users);
  const first=await source.list(id,query),last=await source.list(id,{page:2,page_size:20});
  assert.equal(first.total,'23');assert.equal(first.items.length,20);assert.equal(last.items.length,3);
  const active=await source.list(id,{...query,status:'ACTIVE'}),user=await users.get(id);assert.equal(active.total,user.active_device_count);
  assert.ok(active.items.every(d=>d.status==='ACTIVE'));const revoked=await source.list(id,{...query,status:'REVOKED'});assert.equal(revoked.total,'22');
  const second=await source.list(String(BigInt(id)+1n),query);const ids=new Set([...first.items,...last.items].map(d=>d.id));assert.ok(second.items.every(d=>!ids.has(d.id)));
  first.items[0].display_name='changed locally';assert.equal((await source.list(id,query)).items[0].display_name,'我的手机');
  assert.equal((await source.list(String(BigInt(id)+2n),query)).total,'0');
});
test('offline device preview rejects unknown users, revoked permission and cancellation',async()=>{
  const owner=session(),users=makePreviewUsers(owner),source=makePreviewUserDevices(owner,users);
  await assert.rejects(source.list('42',query),e=>e.status===404);
  const controller=new AbortController();controller.abort();await assert.rejects(source.list(id,query,controller.signal),{name:'AbortError'});
  owner.permissions=[];await assert.rejects(source.list(id,query),e=>e.status===403);
});
