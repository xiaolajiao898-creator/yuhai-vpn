import test from 'node:test';
import assert from 'node:assert/strict';
import {heartbeatAt,readManagedNode,readNodePage,readNodeDetail,nodeQueryProblem,liveNodeSource,boundedNodeRead} from '../src/api/nodes.ts';
import {makePreviewNodes} from '../src/preview/nodes.ts';
import {createLatestRead,stateForRead} from '../src/pages/latest-read.ts';
const observed='2026-09-13T01:00:00Z',id='9007199254740993',query={page:1,page_size:20};
const node=()=>({id,name:'香港 · 01',group:{id:'1',name:'标准线路',min_tier:1},country_code:'HK',city:'香港',region:'ASIA',status:'ONLINE',is_recommended:true,capacity_users:500,online_count:26,load_score:30,last_heartbeat_at:observed,heartbeat_state:'FRESH',agent_version:'1.0.0',updated_at:observed});
const page=(n=node())=>({items:[n],total:'1',...query,observed_at:observed});
const inbound=()=>({id:'9007199254740994',protocol:'WIREGUARD',port:51820,priority:100,status:'ENABLED'});
const detail=()=>({node:node(),inbounds:[inbound()],observed_at:observed,sync_status:'UNAVAILABLE'});
const session=()=>({id:'18',username:'tester',permissions:['nodes.read'],csrf_token:''});
const response=v=>new Response(JSON.stringify(v));
function fetchMock(t,fn){const old=globalThis.fetch;globalThis.fetch=fn;t.after(()=>globalThis.fetch=old);}

test('heartbeat boundaries preserve nanoseconds, missing and future values',()=>{
  assert.equal(heartbeatAt(null,observed),'NEVER');
  assert.equal(heartbeatAt('2026-09-13T00:58:30Z',observed),'FRESH');
  assert.equal(heartbeatAt('2026-09-13T00:58:29.999999999Z',observed),'STALE');
  assert.equal(heartbeatAt('2026-09-13T01:00:00.000000001Z',observed),'CLOCK_SKEW');
  assert.equal(heartbeatAt('2026-09-13T09:00:00+08:00',observed),'FRESH');
  for(const bad of ['2026-02-30T00:00:00Z','2026-09-13T25:00:00Z','2026-09-13','bad',undefined])assert.throws(()=>heartbeatAt(bad,observed));
});
test('node DTO excludes secrets and rejects invalid states while preserving large IDs and missing agent',()=>{
  assert.equal(readManagedNode(node(),observed).id,id);
  assert.equal(readManagedNode({...node(),last_heartbeat_at:null,heartbeat_state:'NEVER',agent_version:null},observed).agent_version,null);
  for(const change of [{id:9007199254740993},{id:'9223372036854775808'},{name:'x\n'},{group:{id:'1',name:'g',min_tier:10}},{country_code:'hk'},{host:'private'},{agent_cert_fingerprint:'secret'},{online_count:-1},{online_count:'26'},{load_score:101},{capacity_users:2147483648},{heartbeat_state:'STALE'},{agent_version:undefined},{updated_at:'2026-02-30T00:00:00Z'}])assert.throws(()=>readManagedNode({...node(),...change},observed));
  // Configured online with no heartbeat remains visibly unknown, never upgraded.
  assert.equal(readManagedNode({...node(),last_heartbeat_at:null,heartbeat_state:'NEVER'},observed).status,'ONLINE');
});
test('node pagination requires matching filters, unique rows and consistent total',()=>{
  assert.equal(readNodePage(page(),query).total,'1');
  for(const change of [{total:'2'},{total:1},{page:2},{page_size:10},{items:[node(),node()],total:'2'},{items:[{...node(),country_code:'JP'}]},{token:'secret'}])assert.throws(()=>readNodePage({...page(),...change},{...query,country_code:'HK'}));
  assert.throws(()=>readNodePage(page(),{...query,status:'OFFLINE'}));
  assert.equal(readNodePage({...page(),items:[],total:'21',page:3},{...query,page:3}).items.length,0);
});
test('node detail binds target and rejects fabricated sync acknowledgements or credentials',()=>{
  assert.equal(readNodeDetail(detail(),id).inbounds[0].id,'9007199254740994');
  for(const changed of [{sync_status:'SYNCED'},{node:{...node(),id:'2'}},{inbounds:[{...inbound(),public_params:{secret:'x'}}]},{inbounds:[{...inbound(),port:65536}]},{inbounds:[{...inbound(),protocol:'UNKNOWN'}]},{inbounds:[inbound(),inbound()]},{inbounds:Array.from({length:101},(_,i)=>({...inbound(),id:String(i+1)}))}])assert.throws(()=>readNodeDetail({...detail(),...changed},id));
  assert.equal(readNodeDetail({...detail(),inbounds:[]},id).inbounds.length,0);
});
test('live nodes source uses exact GET-only requests with cancellation and no mutation headers',async t=>{
  const calls=[];const controller=new AbortController();
  fetchMock(t,async(url,options)=>{calls.push({url,...options});return response(url.includes('?')?page():detail());});
  await liveNodeSource.list({...query,country_code:'HK',search:'香港'},controller.signal);
  await liveNodeSource.get(id,controller.signal);
  assert.equal(calls[0].url,'/admin/v1/nodes?page=1&page_size=20&search=%E9%A6%99%E6%B8%AF&country_code=HK');
  assert.equal(calls[1].url,`/admin/v1/nodes/${id}`);
  for(const call of calls){assert.equal(call.method,'GET');assert.equal(call.signal,controller.signal);assert.equal(call.body,undefined);assert.ok(!('X-Step-Up-Token' in call.headers));assert.ok(!('X-CSRF-Token' in call.headers));}
});
test('invalid node queries never reach transport and server failures are not empty successes',async t=>{
  let calls=0;fetchMock(t,async()=>{calls++;return new Response('{"error":{"message":"denied"}}',{status:403});});
  for(const q of [{...query,page:0},{...query,page_size:51},{...query,search:' '},{...query,search:'foo\n'},{...query,country_code:'hk'},{...query,status:'online'},{...query,secret:'x'}]){assert.ok(nodeQueryProblem(q));await assert.rejects(liveNodeSource.list(q));}
  await assert.rejects(liveNodeSource.get('../1'));assert.equal(calls,0);
  await assert.rejects(liveNodeSource.list(query),e=>e.status===403);assert.equal(calls,1);
});
test('bounded reads abort on timeout without repeating even when transport ignores abort',async()=>{
  let calls=0,signal;
  await assert.rejects(boundedNodeRead(s=>{calls++;signal=s;return new Promise(()=>{});},new AbortController().signal,5),/超时/);
  assert.equal(calls,1);assert.ok(signal.aborted);
});
test('bounded cancellation suppresses late values and never starts already-cancelled reads',async()=>{
  const controller=new AbortController();let finish,calls=0;
  const waiting=boundedNodeRead(()=>{calls++;return new Promise(resolve=>finish=resolve);},controller.signal);
  await Promise.resolve();controller.abort();await assert.rejects(waiting,{name:'AbortError'});finish(page());assert.equal(calls,1);
  await assert.rejects(boundedNodeRead(async()=>{calls++;return page();},controller.signal),{name:'AbortError'});assert.equal(calls,1);
});
test('latest-read channel cannot replace another node or revoked scope with old result',async()=>{
  const published=[];let resolveA;const channel=createLatestRead(s=>published.push(s));
  const a=channel.start('node-a',()=>new Promise(resolve=>resolveA=resolve));
  await channel.start('node-b',async()=>({...detail(),node:{...node(),id:'2'}}));
  resolveA(detail());await a;assert.equal(published.at(-1).key,'node-b');
  assert.deepEqual(stateForRead(published.at(-1),'node-c'),{key:'node-c',phase:'loading'});
  assert.equal(stateForRead(published.at(-1),undefined),undefined);
  channel.cancel();
});
test('offline nodes preserve paging, literal search, snapshot age and response isolation',async()=>{
  let now=Date.parse(observed);const source=makePreviewNodes(session(),()=>now);
  const first=await source.list(query),second=await source.list({...query,page:2});
  assert.equal(first.total,'26');assert.equal(first.items.length,20);assert.equal(second.items.length,6);assert.equal(first.items[0].id,id);
  const active=await source.list({...query,status:'ONLINE',country_code:'HK'});assert.equal(active.total,'6');assert.ok(active.items.every(n=>n.country_code==='HK'&&n.status==='ONLINE'));
  assert.equal((await source.list({...query,search:'%'})).total,'0');
  const profile=await source.get(id);profile.node.name='changed';profile.inbounds.length=0;assert.notEqual((await source.get(id)).node.name,'changed');assert.equal((await source.get(id)).inbounds.length,2);
  const old=first.items.find(n=>n.heartbeat_state==='FRESH');assert.ok(old);now+=300000;
  assert.equal((await source.get(old.id)).node.heartbeat_state,'STALE');
});
test('offline nodes deny revoked permissions and missing IDs rather than creating records',async()=>{
  const owner=session(),source=makePreviewNodes(owner);await assert.rejects(source.get('123'),e=>e.status===404);
  const controller=new AbortController();controller.abort();await assert.rejects(source.list(query,controller.signal),{name:'AbortError'});
  owner.permissions=[];await assert.rejects(source.list(query),e=>e.status===403);await assert.rejects(source.get(id),e=>e.status===403);
});
