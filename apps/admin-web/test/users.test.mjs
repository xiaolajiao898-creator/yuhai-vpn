import test from 'node:test';
import assert from 'node:assert/strict';
import {liveUserSource,moneyMicro,readManagedUser,readUserDetail,readBalanceEntry,readEntitlementEntry,signedLedgerInteger,userQueryProblem} from '../src/api/users.ts';
import {formatUSDT,formatBytes,entitlementDelta,userPageTotal,userReadError} from '../src/pages/users-model.ts';
import {createLatestRead,stateForRead} from '../src/pages/latest-read.ts';
import {makePreviewUsers} from '../src/preview/users.ts';

const uid='9007199254740993';
const date='2026-09-12T00:00:00Z';
const summary=()=>({id:uid,account_no:'8600888000',status:'ACTIVE',balance:'1200.000001',currency:'USDT',active_device_count:'3',created_at:date,updated_at:date});
const subscription=()=>({plan_id:'11',tier:2,expires_at:date,device_limit:3,period_quota_bytes:'107374182400',period_used_bytes:'18790481920',period_reset_at:date});
const detail=()=>({...summary(),invite_code:'YH2345',balance_updated_at:date,subscription:subscription()});
const entry=()=>({id:'9007199254800001',entry_type:'PURCHASE',amount:'-2.500000',balance_after:'1197.500001',source_type:'ORDER',source_id:'9007199254740995',operator_type:'USER',created_at:date});
const page=(items,total=String(items.length),p=1,size=20)=>({items,total,page:p,page_size:size});
const balancePage=()=>({...page([entry()]),user_id:uid,type:'balance',currency:'USDT'});
const session={id:'18',username:'tester',permissions:['users.read','users.ledgers.read'],csrf_token:''};
const json=value=>new Response(JSON.stringify(value));
function fakeFetch(t,fn){const original=globalThis.fetch;globalThis.fetch=fn;t.after(()=>{globalThis.fetch=original;});}
function deferred(){let resolve,reject;const promise=new Promise((a,b)=>{resolve=a;reject=b;});return {promise,resolve,reject};}

test('user balance formatting is exact through int64 micro limits without grouping or floats',()=>{
  assert.equal(moneyMicro('9223372036854.775807'),9223372036854775807n);
  assert.equal(moneyMicro('-9223372036854.775808',true),-9223372036854775808n);
  for(const value of ['9223372036854.775808','-9223372036854.775809','1.00','01.000000','1e3','+1.000000','-0.000000',1])assert.equal(moneyMicro(value,true),null);
  assert.equal(formatUSDT('1200.000000'),'1200');assert.equal(formatUSDT('0.000001'),'0.000001');
  assert.equal(formatUSDT('9223372036854.775807'),'9223372036854.775807');
  assert.equal(formatUSDT('-9223372036854.775808',true),'-9223372036854.775808');
  assert.equal(formatUSDT('1200.000001',true),'+1200.000001');assert.equal(formatUSDT('0.000000'),'0');assert.equal(formatUSDT(null),'—');
  assert.equal(formatBytes('107374182400'),'100 GB');assert.equal(formatBytes('18790481920'),'17.5 GB');assert.equal(formatBytes('0'),'0 B');
  assert.equal(userPageTotal('9223372036854775807',20),200000);
});
test('strict user DTO distinguishes missing balance from zero and excludes unexpected sensitive fields',()=>{
  assert.equal(readManagedUser(summary()).id,uid);
  assert.equal(readManagedUser({...summary(),balance:null}).balance,null);
  assert.equal(readUserDetail({...detail(),balance:null,balance_updated_at:null,subscription:null}).subscription,null);
  assert.equal(readUserDetail({...detail(),balance:'0.000000',subscription:null}).balance,'0.000000');
  for(const change of [{id:9007199254740992},{account_no:'0600888000'},{status:['ACTIVE']},{balance:1200},{balance:'1200.00'},{currency:'USD'},{active_device_count:3},{created_at:'yesterday'},{secret_lookup:'never return'}])assert.throws(()=>readManagedUser({...summary(),...change}));
  for(const change of [{invite_code:''},{invite_code:'A'.repeat(13)},{balance_updated_at:null},{subscription:undefined},{secret_version:2}])assert.throws(()=>readUserDetail({...detail(),...change}));
  for(const change of [{tier:10},{tier:'2'},{device_limit:-1},{period_used_bytes:18790481920},{period_quota_bytes:'-1'},{plan_id:11},{expires_at:'expired'},{internal_token:'x'}])assert.throws(()=>readUserDetail({...detail(),subscription:{...subscription(),...change}}));
});
test('strict balance entries keep signed exact values and reject malformed data instead of inventing rows',()=>{
  assert.equal(readBalanceEntry(entry()).amount,'-2.500000');
  for(const change of [{id:9},{amount:'0.000000'},{amount:2.5},{balance_after:'-1.000000'},{entry_type:'UNKNOWN'},{source_id:null},{source_type:'INVALID'},{operator_type:'OWNER'},{created_at:'1'},{remark:'must not leak'}])assert.throws(()=>readBalanceEntry({...entry(),...change}));
});
test('user API uses only GET, exact filters, exact target IDs and the caller cancellation signal',async t=>{
  const calls=[],controller=new AbortController();
  fakeFetch(t,async(url,options)=>{calls.push({url,...options});return json(url.includes('/ledgers?')?balancePage():url.includes('?')?page([summary()]):detail());});
  assert.equal((await liveUserSource.list({page:1,page_size:20,account_no:'8600888000',status:'ACTIVE'},controller.signal)).items[0].account_no,'8600888000');
  assert.equal((await liveUserSource.get(uid,controller.signal)).id,uid);
  assert.equal((await liveUserSource.ledgers(uid,{page:1,page_size:20},controller.signal)).user_id,uid);
  assert.equal(calls[0].url,'/admin/v1/users?page=1&page_size=20&account_no=8600888000&status=ACTIVE');
  assert.equal(calls[2].url,`/admin/v1/users/${uid}/ledgers?type=balance&page=1&page_size=20`);
  assert.ok(calls.every(c=>c.method==='GET'&&c.body===undefined&&c.signal===controller.signal&&!('X-Step-Up-Token' in c.headers)));
});
test('invalid searches, pagination and targets never send requests',async t=>{
  let count=0;fakeFetch(t,async()=>{count++;return json({});});
  for(const query of [{page:0,page_size:20},{page:10001,page_size:20},{page:1,page_size:51},{page:1,page_size:20,account_no:'8600'},{page:1,page_size:20,account_no:'86008880001'},{page:1,page_size:20,status:'FROZEN'}]){
    assert.ok(userQueryProblem(query));await assert.rejects(liveUserSource.list(query));
  }
  await assert.rejects(liveUserSource.get('../7'));await assert.rejects(liveUserSource.get('9223372036854775808'));
  await assert.rejects(liveUserSource.ledgers(uid,{page:1.5,page_size:20}));assert.equal(count,0);
});
test('user API rejects successful empty or HTML replies and mismatched filters/pages/details',async t=>{
  let value={};fakeFetch(t,async()=>value==='html'?new Response('<html>gateway</html>'):json(value));
  for(const result of [{},'html',page([{...summary(),status:'SUSPENDED'}]),page([summary()], '1',2),page([summary(),summary()]),page([summary()],'0')]){
    value=result;await assert.rejects(liveUserSource.list({page:1,page_size:20,status:'ACTIVE'}));
  }
  value=page([{...summary(),account_no:'8600888001'}]);await assert.rejects(liveUserSource.list({page:1,page_size:20,account_no:'8600888000'}));
  value={...detail(),id:'42'};await assert.rejects(liveUserSource.get(uid));
  value=page([],'0');assert.deepEqual((await liveUserSource.list({page:1,page_size:20})).items,[]);
});
test('ledger response must belong to the selected user, currency, ledger kind and page',async t=>{
  let value=balancePage();fakeFetch(t,async()=>json(value));
  for(const change of [{user_id:'42'},{type:'entitlement'},{currency:'USD'},{page:2},{items:[entry(),entry()]},{items:[{...entry(),amount:'wrong'}]}]){
    value={...balancePage(),...change};await assert.rejects(liveUserSource.ledgers(uid,{page:1,page_size:20}));
  }
  value={...balancePage(),items:[],total:'0'};assert.equal((await liveUserSource.ledgers(uid,{page:1,page_size:20})).total,'0');
});
test('latest read aborts the old account and ignores its late success or failure',async()=>{
  const states=[],channel=createLatestRead(s=>states.push(s)),a=deferred(),b=deferred();let signalA;
  const first=channel.start('user-A',signal=>{signalA=signal;return a.promise;});
  const second=channel.start('user-B',()=>b.promise);assert.equal(signalA.aborted,true);
  b.resolve({account:'B'});await second;a.resolve({account:'A'});await first;
  assert.deepEqual(states.filter(s=>s.phase==='ready'),[{key:'user-B',phase:'ready',value:{account:'B'}}]);
  const c=deferred(),third=channel.start('page-1',()=>c.promise);const fourth=channel.start('page-2',async()=>({page:2}));
  await fourth;c.reject(new Error('old page failed'));await third;
  assert.equal(states.at(-1).key,'page-2');assert.equal(states.at(-1).phase,'ready');assert.ok(!states.some(s=>s.phase==='error'));
});
test('closing or losing access cancels delivery and the next read clears errors before retry',async()=>{
  const states=[],channel=createLatestRead(s=>states.push(s)),pending=deferred();let signal;
  const run=channel.start('ledger-A',s=>{signal=s;return pending.promise;});channel.cancel();assert.equal(signal.aborted,true);
  pending.resolve({secret:'must not appear after close'});await run;assert.equal(states.length,1);
  await channel.start('ledger-A',async()=>{throw new Error('offline');});assert.equal(states.at(-1).phase,'error');
  await channel.start('ledger-A:retry',async()=>({items:[]}));assert.deepEqual(states.slice(-2),[{key:'ledger-A:retry',phase:'loading'},{key:'ledger-A:retry',phase:'ready',value:{items:[]}}]);
  assert.deepEqual(stateForRead({key:'A',phase:'ready',value:{id:'A'}},'B'),{key:'B',phase:'loading'});
  assert.equal(stateForRead({key:'A',phase:'ready',value:{id:'A'}},undefined),undefined);
});
test('offline users preview supports exact search, multiple pages, missing balance and coherent ledgers',async t=>{
  fakeFetch(t,async()=>{throw new Error('preview must stay offline');});const source=makePreviewUsers(session);
  const a=await source.list({page:1,page_size:20}),b=await source.list({page:2,page_size:20});
  assert.equal(a.total,'26');assert.equal(a.items.length,20);assert.equal(b.items.length,6);
  assert.equal(new Set([...a.items,...b.items].map(x=>x.id)).size,26);a.items.forEach(readManagedUser);
  const filtered=await source.list({page:1,page_size:20,account_no:'8600888000',status:'ACTIVE'});assert.equal(filtered.items.length,1);
  const user=readUserDetail(await source.get(filtered.items[0].id));
  const p1=await source.ledgers(user.id,{page:1,page_size:20}),p2=await source.ledgers(user.id,{page:2,page_size:20});
  assert.equal(p1.total,'31');assert.equal(p1.items.length,20);assert.equal(p2.items.length,11);
  const entries=[...p1.items,...p2.items];assert.equal(new Set(entries.map(e=>e.id)).size,31);entries.forEach(readBalanceEntry);
  let sum=0n;for(const e of [...entries].reverse()){sum+=moneyMicro(e.amount,true);assert.equal(moneyMicro(e.balance_after),sum);}
  assert.equal(moneyMicro(user.balance),sum);assert.equal(p1.items[0].balance_after,user.balance);
  const missing=await source.get(a.items[1].id);assert.equal(missing.balance,null);assert.equal(missing.balance_updated_at,null);assert.equal(missing.subscription,null);
  assert.equal((await source.ledgers(missing.id,{page:1,page_size:20})).total,'0');
  const zero=await source.get(a.items[2].id);assert.equal(zero.balance,'0.000000');
  const notFound=await source.list({page:1,page_size:20,account_no:'9999999999'});assert.equal(notFound.total,'0');
  a.items[0].balance='9.000000';assert.equal((await source.get(user.id)).balance,user.balance);
});
test('preview users enforces separate ledger permission and cancellation before target lookup',async()=>{
  const noAccess=makePreviewUsers({...session,permissions:[]}),readOnly=makePreviewUsers({...session,permissions:['users.read']});
  await assert.rejects(noAccess.get('42'),e=>e.status===403);
  assert.equal((await readOnly.get(uid)).id,uid);await assert.rejects(readOnly.ledgers(uid,{page:1,page_size:20}),e=>e.status===403);
  const controller=new AbortController();controller.abort();await assert.rejects(readOnly.list({page:1,page_size:20},controller.signal),e=>e.name==='AbortError');
  assert.match(userReadError({status:403}),/权限/);assert.match(userReadError({status:404}),/未找到/);assert.match(userReadError({status:401}),/登录/);
});


const entitlementEntry=()=>({id:'9007199254900026',change_type:'TRAFFIC_PACK',delta_seconds:'0',delta_bytes:'9007199254740993',delta_devices:'0',source_type:'ORDER',source_id:'82026',operator_type:'USER',created_at:date});
const entitlementPage=()=>({...page([entitlementEntry()]),user_id:uid,type:'entitlement',currency:null});
test('entitlement DTO keeps zero distinct from missing and validates signed integer ranges',()=>{
  assert.equal(signedLedgerInteger('9223372036854775807'),9223372036854775807n);
  assert.equal(signedLedgerInteger('-9223372036854775808'),-9223372036854775808n);
  for(const value of [undefined,null,0,'-0','+1','01','1.5','1e3','9223372036854775808','-9223372036854775809'])assert.equal(signedLedgerInteger(value),null);
  const read=readEntitlementEntry(entitlementEntry());assert.equal(read.delta_seconds,'0');assert.equal(read.delta_bytes,'9007199254740993');
  assert.equal(readEntitlementEntry({...entitlementEntry(),source_type:'SYSTEM',source_id:null}).source_id,null);
  assert.equal(readEntitlementEntry({...entitlementEntry(),delta_seconds:'-9223372036854775808',delta_bytes:'9223372036854775807',delta_devices:'-2147483648'}).delta_devices,'-2147483648');
  for(const delta of ['delta_seconds','delta_bytes','delta_devices']){
    const missing=entitlementEntry();delete missing[delta];assert.throws(()=>readEntitlementEntry(missing));
    for(const wrong of [null,0,''])assert.throws(()=>readEntitlementEntry({...entitlementEntry(),[delta]:wrong}));
  }
  for(const change of [{id:9},{change_type:'UNKNOWN'},{source_type:'UNKNOWN'},{source_id:'0'},{source_id:82026},{operator_type:'OWNER'},{delta_devices:'2147483648'},{delta_devices:'-2147483649'},{before_snapshot:{}},{created_at:'today'}])assert.throws(()=>readEntitlementEntry({...entitlementEntry(),...change}));
});
test('entitlement changes format exact durations and binary traffic with visible signs',()=>{
  assert.deepEqual(entitlementDelta('2592000','seconds'),{label:'+30 天',exact:'+2592000 秒',direction:'positive'});
  assert.deepEqual(entitlementDelta('-90061','seconds'),{label:'-1 天 1 小时 1 分 1 秒',exact:'-90061 秒',direction:'negative'});
  assert.equal(entitlementDelta('0','seconds').label,'0 秒');assert.equal(entitlementDelta('0','bytes').label,'0 B');assert.equal(entitlementDelta('0','devices').label,'0 台');
  assert.equal(entitlementDelta(undefined,'bytes').direction,'missing');assert.notEqual(entitlementDelta(undefined,'bytes').label,entitlementDelta('0','bytes').label);
  assert.deepEqual(entitlementDelta('9007199254740993','bytes'),{label:'约 +8388608 GiB',exact:'+9007199254740993 字节',direction:'positive'});
  assert.deepEqual(entitlementDelta('-9223372036854775808','bytes'),{label:'-8589934592 GiB',exact:'-9223372036854775808 字节',direction:'negative'});
  assert.equal(entitlementDelta('-9223372036854775808','seconds').exact,'-9223372036854775808 秒');
  assert.match(entitlementDelta('-9223372036854775808','seconds').label,/^-106751991167300 天/);
  assert.equal(entitlementDelta('1024','bytes').label,'+1 KiB');assert.equal(entitlementDelta('-1048576','bytes').label,'-1 MiB');
  assert.equal(entitlementDelta('2','devices').label,'+2 台');assert.equal(entitlementDelta('-1','devices').label,'-1 台');
  assert.equal(entitlementDelta('2147483648','devices').direction,'missing');
});
test('entitlement adapter sends only the selected user GET with type and cancellation signal',async t=>{
  const calls=[],controller=new AbortController();fakeFetch(t,async(url,options)=>{calls.push({url,...options});return json({...entitlementPage(),page:2});});
  const result=await liveUserSource.entitlements(uid,{page:2,page_size:20},controller.signal);
  assert.equal(result.type,'entitlement');assert.equal(result.currency,null);assert.equal(result.items[0].delta_bytes,'9007199254740993');
  assert.equal(calls.length,1);assert.equal(calls[0].url,`/admin/v1/users/${uid}/ledgers?type=entitlement&page=2&page_size=20`);
  assert.equal(calls[0].method,'GET');assert.equal(calls[0].body,undefined);assert.equal(calls[0].signal,controller.signal);assert.ok(!('X-Step-Up-Token' in calls[0].headers));
  await assert.rejects(liveUserSource.entitlements('../2',{page:1,page_size:20}));await assert.rejects(liveUserSource.entitlements(uid,{page:10001,page_size:20}));assert.equal(calls.length,1);
});
test('entitlement adapter rejects cross-account, wrong kind, malformed success and missing fields',async t=>{
  let value=entitlementPage(),status=200;fakeFetch(t,async()=>value==='html'?new Response('<html>not json</html>'):new Response(JSON.stringify(value),{status}));
  for(const change of [{user_id:'42'},{type:'balance'},{currency:'USDT'},{currency:undefined},{page:2},{items:[entitlementEntry(),entitlementEntry()]},{items:[{...entitlementEntry(),delta_seconds:undefined}]},{items:[{...entitlementEntry(),delta_bytes:9007199254740992}]}]){
    value={...entitlementPage(),...change};await assert.rejects(liveUserSource.entitlements(uid,{page:1,page_size:20}));
  }
  for(const bad of [{},'html']){value=bad;await assert.rejects(liveUserSource.entitlements(uid,{page:1,page_size:20}));}
  status=403;value={error:{message:'缺少流水查看权限'}};await assert.rejects(liveUserSource.entitlements(uid,{page:1,page_size:20}),e=>e.status===403&&userReadError(e).includes('权限'));
  status=200;value={...entitlementPage(),items:[],total:'0'};assert.equal((await liveUserSource.entitlements(uid,{page:1,page_size:20})).total,'0');
});
test('entitlement preview has independent complete pages and exact increases, decreases and zero dimensions',async t=>{
  fakeFetch(t,async()=>{throw new Error('preview must stay offline');});const source=makePreviewUsers(session);
  const first=await source.entitlements(uid,{page:1,page_size:20}),second=await source.entitlements(uid,{page:2,page_size:20});
  assert.equal(first.user_id,uid);assert.equal(first.type,'entitlement');assert.equal(first.currency,null);assert.equal(first.total,'27');assert.equal(first.items.length,20);assert.equal(second.items.length,7);
  const all=[...first.items,...second.items];assert.equal(new Set(all.map(e=>e.id)).size,27);all.forEach(readEntitlementEntry);
  assert.ok(all.some(e=>e.delta_seconds==='2592000'));assert.ok(all.some(e=>e.delta_seconds==='-604800'));
  assert.ok(all.some(e=>e.delta_bytes==='9007199254740993'));assert.ok(all.some(e=>e.delta_bytes==='-9007199254740993'));
  assert.ok(all.some(e=>e.delta_devices==='2'));assert.ok(all.some(e=>e.delta_devices==='-1'));
  assert.ok(all.some(e=>e.delta_seconds==='0'&&e.delta_bytes==='0'&&e.delta_devices==='0'));
  for(let i=1;i<all.length;i++)assert.ok(BigInt(all[i-1].id)>BigInt(all[i].id));
  first.items[0].delta_bytes='0';assert.equal((await source.entitlements(uid,{page:1,page_size:20})).items[0].delta_bytes,'9007199254740993');
  const empty=await source.entitlements('9007199254740994',{page:1,page_size:20});assert.equal(empty.total,'0');assert.deepEqual(empty.items,[]);
  const balance=await source.ledgers(uid,{page:1,page_size:20});assert.equal(balance.type,'balance');assert.equal(balance.total,'31');
});
test('entitlement preview enforces separate read permission, abort and missing users',async()=>{
  const source=makePreviewUsers(session),limited=makePreviewUsers({...session,permissions:['users.read']});
  await assert.rejects(limited.entitlements('42',{page:1,page_size:20}),e=>e.status===403);
  await assert.rejects(source.entitlements('42',{page:1,page_size:20}),e=>e.status===404);
  const controller=new AbortController();controller.abort();await assert.rejects(source.entitlements(uid,{page:1,page_size:20},controller.signal),e=>e.name==='AbortError');
});
test('switching balance, entitlement tabs and users cancels old reads including delayed errors',async()=>{
  const states=[],channel=createLatestRead(s=>states.push(s)),balance=deferred(),rightsA=deferred(),rightsB=deferred();let balanceSignal,rightsSignal;
  const first=channel.start('A:balance:1',signal=>{balanceSignal=signal;return balance.promise;});
  const second=channel.start('A:entitlement:1',signal=>{rightsSignal=signal;return rightsA.promise;});assert.equal(balanceSignal.aborted,true);
  const third=channel.start('B:entitlement:1',()=>rightsB.promise);assert.equal(rightsSignal.aborted,true);
  rightsB.resolve({user_id:'B',items:[]});await third;balance.resolve({user_id:'A',type:'balance'});rightsA.reject(new Error('old account denied'));await Promise.all([first,second]);
  assert.deepEqual(states.filter(s=>s.phase==='ready'),[{key:'B:entitlement:1',phase:'ready',value:{user_id:'B',items:[]}}]);assert.ok(!states.some(s=>s.phase==='error'));
  assert.deepEqual(stateForRead(states.at(-1),'B:entitlement:2'),{key:'B:entitlement:2',phase:'loading'});
});
