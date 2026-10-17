import test from 'node:test';
import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import {liveVersionSource,readVersion,sameVersionFields,VersionWriteError} from '../src/api/versions.ts';
import {draftProblem,normalizeDraft,publicDownloadURL,versionBlock} from '../src/pages/versions-model.ts';
import {makePreviewSource} from '../src/preview/source.ts';
const operation='c2e0c235-838e-446a-bd88-ff226530a68b';
const fields=()=>({platform:'ANDROID',version_name:'1.4.0',version_code:'140',min_supported_code:'100',download_url:'https://downloads.example.com/yuhai.apk',file_sha256:'a'.repeat(64),cert_sha256:'b'.repeat(64),release_notes:'改善版本提示，支持设备管理。'});
const version=()=>({...fields(),id:'9007199254740993',status:'DRAFT',published_at:null,approval_id:null,created_at:'2026-09-12T00:00:00Z',snapshot_hash:'f'.repeat(64),pending_approval_id:null,can_edit:true});
const session={id:'18',username:'tester',permissions:['app_versions.read','app_versions.draft','app_versions.publish','approvals.read','approvals.decide','audit.read'],csrf_token:''};
const token=()=>({step_up_token:'x'.repeat(43),expires_at:'2099-09-12T00:00:00Z'});
const response=value=>new Response(JSON.stringify(value));
function fakeFetch(t,fn){const original=globalThis.fetch;globalThis.fetch=fn;t.after(()=>{globalThis.fetch=original;});}

test('version fields enforce exact numbers, package hashes and platform metadata',()=>{
  assert.equal(draftProblem(fields()),'');assert.equal(draftProblem({...fields(),platform:'IOS',cert_sha256:null}),'');
  for(const input of [{version_code:'00140'},{version_code:'2147483648'},{version_code:'1,400'},{min_supported_code:'141'},{min_supported_code:'-1'},{cert_sha256:null},{file_sha256:'no'},{version_name:'x'.repeat(33)},{release_notes:'中'.repeat(5334)}])assert.notEqual(draftProblem({...fields(),...input}), '');
  assert.equal(draftProblem({...fields(),release_notes:'中'.repeat(5333)}),'');
  assert.equal(normalizeDraft({...fields(),file_sha256:'A'.repeat(64)}).file_sha256,'a'.repeat(64));
  for(const url of ['http://downloads.example.com/a','https://user:pass@example.com/a','https://example.com/a?key=abc','https://example.com/a#x','https://localhost/a','https://127.0.0.1/a','https://10.1.2.3/a','https://169.254.1.2/a','https://x.internal/a','https://8.8.8.8/a','https://[2606:4700:4700::1111]/a','https://example.com:0/a','https://2130706433/a','https://example.com\\@localhost/a'])assert.equal(publicDownloadURL(url),false,url);
});
test('strict read keeps bigint IDs, nullable legacy metadata and status guards',()=>{
  assert.equal(readVersion(version()).id,'9007199254740993');
  assert.equal(readVersion({...version(),download_url:null,file_sha256:null,cert_sha256:null}).download_url,null);
  assert.equal(readVersion({...version(),approval_id:'1',can_edit:false}).can_edit,false);
  for(const input of [{platform:['ANDROID']},{status:['DRAFT'],can_edit:false},{id:9007199254740992},{id:'9223372036854775808'},{can_edit:true,pending_approval_id:'12'},{file_sha256:4},{created_at:'invalid'},{snapshot_hash:'abc'},{min_supported_code:'141'}])assert.throws(()=>readVersion({...version(),...input}));
  assert.match(versionBlock(version(),{...session,permissions:[]},'edit'),/权限/);
  assert.match(versionBlock({...version(),pending_approval_id:'32',can_edit:false},session,'edit'),/待审批/);
  assert.match(versionBlock({...version(),status:'PUBLISHED',can_edit:false},session,'publish'),/不能/);
  assert.match(versionBlock({...version(),download_url:null},session,'publish'),/HTTPS/);
  assert.match(versionBlock({...version(),approval_id:'1',can_edit:false},session,'publish'),/核实/);
});
test('create confirmation freezes exact submitted body and uses draft stepup hash',async t=>{
  const calls=[];fakeFetch(t,async(url,options)=>{calls.push({url,...options});return url==='/admin/v1/step-up'?response(token()):response(version());});
  const input={...fields(),operation_id:operation};const request=await liveVersionSource.prepareSave(input);
  input.version_name='changed';input.download_url='https://other.example.com/package';
  assert.equal((await request.confirm('123456')).version_name,'1.4.0');
  assert.equal(calls.length,2);assert.equal(calls[1].url,'/admin/v1/app-versions');assert.equal(calls[1].method,'POST');
  const raw=JSON.stringify({...fields(),operation_id:operation});assert.equal(calls[1].body,raw);
  const proof=JSON.parse(calls[0].body);assert.equal(proof.action_code,'app_versions.draft');assert.equal(proof.payload_hash,createHash('sha256').update(`v1\nPOST\n/admin/v1/app-versions\n${raw}`).digest('hex'));
  await assert.rejects(request.confirm('654321'));assert.equal(calls.length,2);
});
test('edit locks target, expected snapshot and replacement fields at preparation',async t=>{
  const calls=[];fakeFetch(t,async(url,options)=>{calls.push({url,...options});return url.endsWith('/step-up')?response(token()):response(version());});
  const base={id:version().id,snapshot_hash:version().snapshot_hash};
  const request=await liveVersionSource.prepareSave({...fields(),operation_id:operation},base);
  base.id='20';base.snapshot_hash='1'.repeat(64);
  assert.equal((await request.confirm('123456')).id,version().id);
  assert.equal(calls[1].method,'PUT');assert.equal(calls[1].url,`/admin/v1/app-versions/${version().id}`);
  assert.equal(JSON.parse(calls[1].body).expected_snapshot_hash,'f'.repeat(64));
});
test('uncertain save and concurrent confirms never repeat mutation',async t=>{
  const calls=[];fakeFetch(t,async(url)=>{calls.push(url);if(url.endsWith('/step-up'))return response(token());throw new Error('lost receipt');});
  const request=await liveVersionSource.prepareSave({...fields(),operation_id:operation});
  const results=await Promise.allSettled(Array.from({length:8},()=>request.confirm('123456')));
  assert.equal(results.filter(r=>r.status==='fulfilled').length,0);assert.equal(calls.length,2);
  assert.ok(results.some(r=>r.reason instanceof VersionWriteError&&r.reason.uncertain));
  await assert.rejects(request.confirm('654321'),error=>error.uncertain===true);assert.equal(calls.length,2);
});
test('failed verification sends no version mutation and permits explicit fresh confirmation',async t=>{
  const calls=[];let failed=true;fakeFetch(t,async(url)=>{calls.push(url);if(url.endsWith('/step-up'))return failed?new Response(JSON.stringify({error:{message:'动态验证码错误'}}),{status:401}):response(token());return response(version());});
  const input={...fields(),operation_id:operation};const request=await liveVersionSource.prepareSave(input);
  await assert.rejects(request.confirm('123456'),error=>error instanceof VersionWriteError&&!error.uncertain);
  assert.deepEqual(calls,['/admin/v1/step-up']);failed=false;
  const fresh=await liveVersionSource.prepareSave(input);await fresh.confirm('654321');assert.equal(calls.length,3);
});
test('HTTP 200 invalid, changed or wrong-state draft receipts are uncertain, never success',async t=>{
  let receipt={};fakeFetch(t,async(url)=>url.endsWith('/step-up')?response(token()):receipt==='html'?new Response('<html>gateway</html>'):response(receipt));
  for(const value of [{},'html',{...version(),version_code:'141'},{...version(),status:'PUBLISHED',can_edit:false,published_at:'2026-09-12T00:01:00Z',approval_id:'12'}]){
    receipt=value;const request=await liveVersionSource.prepareSave({...fields(),operation_id:operation});await assert.rejects(request.confirm('123456'),error=>error.uncertain===true);
  }
});
test('readonly recovery queries exact platform/code then matching detail without any write',async t=>{
  const calls=[];fakeFetch(t,async(url,options)=>{calls.push({url,...options});return url.includes('?')?response({items:[version()],total:'1',page:1,page_size:10}):response(version());});
  const result=await liveVersionSource.find('ANDROID','140');assert.ok(sameVersionFields(result,fields()));
  assert.match(calls[0].url,/platform=ANDROID&version_code=140/);assert.equal(calls[1].url,`/admin/v1/app-versions/${version().id}`);assert.ok(calls.every(c=>c.method==='GET'));
});
test('recovery rejects nonmatching list/filter and does not invent missing records',async t=>{
  let data={items:[],total:'0',page:1,page_size:10};fakeFetch(t,async()=>response(data));
  assert.equal(await liveVersionSource.find('ANDROID','140'),null);
  data={items:[{...version(),version_code:'141'}],total:'1',page:1,page_size:10};await assert.rejects(liveVersionSource.find('ANDROID','140'));
  data={items:[version()],total:'1',page:2,page_size:10};await assert.rejects(liveVersionSource.find('ANDROID','140'));
  await assert.rejects(liveVersionSource.list({page:1,page_size:20,version_code:'140'}),/平台/);
});
test('publish is an approval request bound to the displayed version snapshot',async t=>{
  const calls=[];const v=version();let receipt={id:'33',action_code:'app_versions.publish',target_type:'app_version',target_id:v.id,status:'PENDING',reason:'发布安全更新',requested_by:'18',created_at:'2026-09-12T00:00:00Z',expires_at:'2099-09-13T00:00:00Z',payload_hash:v.snapshot_hash,executor_supported:true};
  fakeFetch(t,async(url,options)=>{calls.push({url,...options});return url.endsWith('/step-up')?response(token()):response(receipt);});
  const request=await liveVersionSource.preparePublish(v,'发布安全更新');v.snapshot_hash='2'.repeat(64);
  assert.equal((await request.confirm('123456')).status,'PENDING');assert.equal(calls[1].url,`/admin/v1/app-versions/${v.id}/publish`);assert.deepEqual(JSON.parse(calls[1].body),{reason:'发布安全更新'});assert.equal(JSON.parse(calls[0].body).action_code,'app_versions.publish');
  receipt={...receipt,payload_hash:'0'.repeat(64)};const changed=await liveVersionSource.preparePublish(version(),'发布安全更新');await assert.rejects(changed.confirm('123456'),error=>error.uncertain===true);
});
test('preview supports draft create/edit/readback/approval and locks pending content offline',async t=>{
  fakeFetch(t,async()=>{throw new Error('preview must stay offline');});const source=makePreviewSource(session);
  const created=await(await source.versions.prepareSave({...fields(),operation_id:operation})).confirm('123456');assert.equal(created.status,'DRAFT');assert.equal(created.can_edit,true);
  const next={...fields(),version_name:'1.4.1',release_notes:'改进登录提示。',operation_id:'e3c43ae2-257a-44dd-b231-a9e9c5e33872'};
  const updated=await(await source.versions.prepareSave(next,{id:created.id,snapshot_hash:created.snapshot_hash})).confirm('123456');assert.equal(updated.version_name,'1.4.1');assert.notEqual(updated.snapshot_hash,created.snapshot_hash);
  assert.equal((await source.versions.find('ANDROID','140')).id,created.id);
  const approval=await(await source.versions.preparePublish(updated,'准备发布更新')).confirm('123456');assert.equal(approval.status,'PENDING');assert.equal(approval.target_id,created.id);
  const latest=await source.versions.get(created.id);assert.equal(latest.can_edit,false);assert.equal(latest.pending_approval_id,approval.id);
  await assert.rejects((await source.versions.prepareSave({...next,operation_id:'c08c0b90-f35d-4d82-8645-89807fa5bd7f'},{id:latest.id,snapshot_hash:latest.snapshot_hash})).confirm('123456'),/待审批/);
  await assert.rejects((await source.prepareDecision(approval.id,'approve','')).confirm('123456'),/本人/);
  assert.ok((await source.listApprovals({page:1,page_size:20,scope:'mine'})).items.some(a=>a.id===approval.id));
  assert.equal((await source.versions.get(created.id)).status,'DRAFT');
});
test('preview prior approvals update shared version state; published draft is immutable',async()=>{
  const source=makePreviewSource(session),before=await source.versions.get('306');assert.equal(before.can_edit,false);
  await(await source.prepareDecision('7306','approve','')).confirm('123456');
  const after=await source.versions.get('306');assert.equal(after.status,'PUBLISHED');assert.equal(after.approval_id,'7306');assert.equal(after.pending_approval_id,null);
  await assert.rejects((await source.versions.prepareSave({...fields(),operation_id:operation},{id:after.id,snapshot_hash:after.snapshot_hash})).confirm('123456'),/不能/);
});
test('preview stale edit and operation replay cannot overwrite newer draft',async()=>{
  const source=makePreviewSource(session);const original=await source.versions.get('307');
  const input={...normalizeDraft(original),version_name:'1.3.1',operation_id:operation};
  const updated=await(await source.versions.prepareSave(input,{id:original.id,snapshot_hash:original.snapshot_hash})).confirm('123456');
  await assert.rejects((await source.versions.prepareSave({...input,operation_id:'141f1712-0b26-4cac-a5d6-112217cc8c1b'},{id:original.id,snapshot_hash:original.snapshot_hash})).confirm('123456'),/变化/);
  const replay=await(await source.versions.prepareSave(input,{id:original.id,snapshot_hash:original.snapshot_hash})).confirm('123456');assert.equal(replay.snapshot_hash,updated.snapshot_hash);
  assert.equal((await source.versions.get('307')).version_name,'1.3.1');
});


test('preview seed audit references existing approvals and cannot preclaim the next request',async t=>{
  fakeFetch(t,async()=>{throw new Error('preview must stay offline');});
  const source=makePreviewSource(session);
  const initialApprovals=(await source.listApprovals({page:1,page_size:50,scope:'all'})).items;
  const initialLogs=(await source.listAudit({page:1,page_size:50})).items;
  const byId=new Map(initialApprovals.map(a=>[a.id,a]));
  assert.equal(initialLogs.length,12);
  for(const log of initialLogs){
    if(log.approval_id===null){
      assert.equal(log.action_code,'app_versions.draft');assert.equal(log.target_type,'app_version');
      const v=await source.versions.get(log.target_id);assert.equal(v.id,log.target_id);
      const laterRequest=initialApprovals.find(a=>a.target_type===log.target_type&&a.target_id===log.target_id);
      assert.equal(log.admin_user_id,laterRequest.requested_by);continue;
    }
    const approval=byId.get(log.approval_id);
    assert.ok(approval,`seed log ${log.id} references missing approval ${log.approval_id}`);
    assert.equal(log.target_id,approval.target_id);assert.equal(log.target_type,approval.target_type);
    assert.equal(log.result,'SUCCESS');
    if(log.action_code==='approvals.create'){
      assert.equal(log.admin_user_id,approval.requested_by);assert.equal(log.created_at,approval.created_at);
    }else{
      assert.ok(['approvals.approve','approvals.reject'].includes(log.action_code));
      assert.equal(approval.status,log.action_code==='approvals.approve'?'EXECUTED':'REJECTED');
      assert.equal(log.admin_user_id,approval.decided_by);assert.notEqual(log.admin_user_id,approval.requested_by);
      assert.equal(log.created_at,approval.decided_at);
    }
  }
  const draft=await source.versions.get('307');
  const created=await(await source.versions.preparePublish(draft,'核对示例版本后申请发布')).confirm('123456');
  assert.equal(created.id,'7307');assert.ok(!byId.has(created.id));
  assert.ok(initialLogs.every(log=>log.approval_id!==created.id),'seed logs must not claim a future request');
  const related=(await source.listAudit({page:1,page_size:50})).items.filter(log=>log.approval_id===created.id);
  assert.equal(related.length,1);assert.equal(related[0].action_code,'approvals.create');
  assert.equal(related[0].target_type,created.target_type);assert.equal(related[0].target_id,created.target_id);
  assert.equal(related[0].admin_user_id,created.requested_by);
  await(await source.prepareDecision('7303','reject','此示例尚未接入执行')).confirm('123456');
  const rejected=(await source.listAudit({page:1,page_size:50})).items[0];
  assert.equal(rejected.approval_id,'7303');assert.equal(rejected.target_type,'invite_rule');
});
