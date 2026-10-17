import test from 'node:test';
import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import {liveReviewSource, readApproval} from '../src/api/review.ts';
import {decisionBlock, validRange, boundedTotal} from '../src/pages/review-model.ts';
import {makePreviewSource} from '../src/preview/source.ts';
const session={id:'18',username:'tester',permissions:['approvals.read','approvals.decide','app_versions.publish','audit.read'],csrf_token:''};
const sample=()=>({id:'9007199254740993',action_code:'app_versions.publish',target_type:'app_version',target_id:'20',status:'PENDING',reason:'安全更新',requested_by:'26',created_at:'2026-09-12T00:00:00Z',expires_at:'2099-09-13T00:00:00Z',payload_hash:'f'.repeat(64),executor_supported:true});
const response=v=>new Response(JSON.stringify(v));
function fakeFetch(t,handler){const original=globalThis.fetch;globalThis.fetch=handler;t.after(()=>{globalThis.fetch=original;});}
test('decision gates cover self, permissions, unsupported action and exact expiry',()=>{
  const a=sample();assert.equal(decisionBlock(a,session,'approve'), '');
  assert.match(decisionBlock({...a,requested_by:'18'},session,'approve'),/本人/);
  assert.match(decisionBlock(a,{...session,permissions:[]},'reject'),/权限/);
  assert.match(decisionBlock({...a,expires_at:'invalid'},session,'approve'),/过期/);
  assert.match(decisionBlock(a,session,'approve',Date.parse(a.expires_at)),/过期/);
  assert.match(decisionBlock({...a,executor_supported:false},session,'approve'),/尚未/);
  assert.equal(decisionBlock(a,{...session,permissions:['approvals.decide']},'reject'),'');
  assert.match(decisionBlock(a,{...session,permissions:['approvals.decide']},'approve'),/执行权限/);
});
test('IDs stay exact above JS safe integers and ranges reject malformed values',()=>{
  assert.equal(readApproval(sample()).id,'9007199254740993');
  for(const pair of [['9007199254740993','9007199254740992'],['0','5'],['1','9223372036854775808'],['01','5'],['1e2','200']]) assert.equal(validRange(...pair),false);
  assert.equal(validRange('9007199254740992','9007199254740993'),true);
  assert.equal(boundedTotal('9007199254740993'),Number.MAX_SAFE_INTEGER);
  assert.throws(()=>readApproval({...sample(),id:9007199254740992}));
});
test('decision freezes body, uses bound hash and only attempts once on uncertain response',async t=>{
  const calls=[];fakeFetch(t,async(url,options)=>{calls.push({url,...options});if(url==='/admin/v1/step-up')return response({step_up_token:'a'.repeat(43)});throw new Error('connection lost');});
  const request=await liveReviewSource.prepareDecision(sample().id,'reject','请补充资料');
  await assert.rejects(request.confirm('123456'),/connection lost/);
  await assert.rejects(request.confirm('654321'),/重新查询/);
  assert.equal(calls.length,2);
  const raw=JSON.stringify({note:'请补充资料'});
  assert.equal(calls[1].body,raw);
  assert.equal(JSON.parse(calls[0].body).payload_hash,createHash('sha256').update(`v1\nPOST\n${calls[1].url}\n${raw}`).digest('hex'));
});
test('successful HTTP without valid matching terminal receipt is never success',async t=>{
  let receipt='html';fakeFetch(t,async(url)=>url==='/admin/v1/step-up'?response({step_up_token:'b'.repeat(43)}):receipt==='html'?new Response('<html>gateway</html>'):response(receipt));
  for(const body of ['html',{},sample(),{...sample(),id:'30',status:'EXECUTED'}, {...sample(),status:'EXECUTED',decided_by:'26',decided_at:'2026-09-12T00:10:00Z'}]){
    receipt=body;const request=await liveReviewSource.prepareDecision(sample().id,'approve','');await assert.rejects(request.confirm('123456'),/数据不完整/);
  }
  receipt={...sample(),status:'EXECUTED',decided_by:'18',decided_at:'2026-09-12T00:10:00Z',executed_at:'2026-09-12T00:10:00Z'};
  const request=await liveReviewSource.prepareDecision(sample().id,'approve','');assert.equal((await request.confirm('123456')).status,'EXECUTED');
});
test('concurrent confirms produce a single mutation',async t=>{
  const calls=[];fakeFetch(t,async(url)=>{calls.push(url);if(url.endsWith('/step-up'))return response({step_up_token:'c'.repeat(43)});return response({...sample(),status:'REJECTED',decided_by:'18',decided_at:'2026-09-12T00:10:00Z',decision_note:'请重填'});});
  const request=await liveReviewSource.prepareDecision(sample().id,'reject','请重填');const results=await Promise.allSettled(Array.from({length:8},()=>request.confirm('123456')));
  assert.equal(results.filter(r=>r.status==='fulfilled').length,1);assert.equal(calls.length,2);
});
test('read endpoints reject malformed pages and mismatched verification range',async t=>{
  let result={};fakeFetch(t,async()=>response(result));
  await assert.rejects(liveReviewSource.listApprovals({page:1,page_size:20,scope:'all'}));
  result={...sample(),id:'20'};await assert.rejects(liveReviewSource.getApproval('21'));
  result={valid:true,from_id:'1',to_id:'2',checked:2,head_id:'2',head_hash:'f'.repeat(64),reason:'ok',external_anchor:false};
  await assert.rejects(liveReviewSource.verify('1','3'));assert.equal((await liveReviewSource.verify('1','2')).valid,true);
});
test('offline preview decisions complete and append exactly one example log',async t=>{
  fakeFetch(t,async()=>{throw new Error('preview must not fetch');});
  const source=makePreviewSource(session);const list=await source.listApprovals({page:1,page_size:20,scope:'pending'});assert.equal(list.items.length,3);
  const a=list.items.find(a=>a.executor_supported);const initial=await source.listAudit({page:1,page_size:20});const request=await source.prepareDecision(a.id,'approve','');
  assert.equal((await request.confirm('123456')).status,'EXECUTED');await assert.rejects(request.confirm('123456'));
  const after=await source.listAudit({page:1,page_size:20});assert.equal(Number(after.total),Number(initial.total)+1);assert.equal(after.items[0].approval_id,a.id);
  assert.equal((await source.getApproval(a.id)).status,'EXECUTED');
  const own=(await source.listApprovals({page:1,page_size:20,scope:'mine'})).items.find(a=>a.status==='PENDING');
  await assert.rejects((await source.prepareDecision(own.id,'approve','')).confirm('123456'),/本人/);
  const unsupported=list.items.find(a=>!a.executor_supported);await assert.rejects((await source.prepareDecision(unsupported.id,'approve','')).confirm('123456'),/尚未/);
  assert.equal((await (await source.prepareDecision(unsupported.id,'reject','未接执行')).confirm('123456')).status,'REJECTED');
});

test('system audit entries may have no actor or target',async t=>{
  const row={id:'1',admin_user_id:null,action_code:'system.check',target_type:null,target_id:null,approval_id:null,request_id:'demo',result:'SUCCESS',step_up_verified:false,created_at:'2026-09-12T00:10:00Z',prev_hash:'0'.repeat(64),row_hash:'a'.repeat(64)};
  fakeFetch(t,async()=>response({items:[row],page:1,page_size:20,total:'1'}));
  assert.equal((await liveReviewSource.listAudit({page:1,page_size:20})).items[0].target_type,null);
});
