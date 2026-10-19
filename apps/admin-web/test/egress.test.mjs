import test from 'node:test';
import assert from 'node:assert/strict';
import {readItem,readPage,readPreview,readSubscription,readImport,optionsProblem,queryProblem,liveEgressSource,validOperationId} from '../src/api/egress.ts';
import {makePreviewEgress} from '../src/preview/egress.ts';

const at='2026-09-28T01:00:00Z';
const item=(p={})=>({id:'e12',name:'新加坡 · 01',protocol:'SOCKS5',host:'203.0.113.10',port:1080,country_code:'SG',city:'新加坡',region:'ASIA',kind:'PROXY',supports_udp:true,capacity_users:0,identity_hint:'al***',params:{source_kind:'IMPORT'},min_tier:1,status:'ENABLED',sort_order:0,source:'IMPORT',subscription_id:null,created_at:at,updated_at:at,...p});
const opts={default_protocol:'SOCKS5',default_udp:false,country_code:'SG',city:'',region:'ASIA',name_prefix:'',min_tier:1,enable:false,update_existing:false};
function fetchMock(t,fn){const old=globalThis.fetch;globalThis.fetch=fn;t.after(()=>globalThis.fetch=old);}

test('出口条目：形状严格，凭据类参数与不一致的 UDP 一律拒绝',()=>{
  assert.equal(readItem(item()).id,'e12');
  for(const bad of [{id:'12'},{id:'e01'},{protocol:'HTTP',supports_udp:true},{capacity_users:-1},{params:{password:'x'}},{params:{uuid:'1'}},{identity_hint:'11111111-2222-4333-8444-555555555555'},{min_tier:0},{status:'ON'},{port:0}]){
    assert.throws(()=>readItem(item(bad)),undefined,JSON.stringify(bad));
  }
});
test('分页：总数、页码与筛选必须和查询一致',()=>{
  const q={page:1,page_size:20,protocol:'SOCKS5'};
  assert.equal(readPage({items:[item()],total:'1',page:1,page_size:20,observed_at:at},q).items.length,1);
  assert.throws(()=>readPage({items:[item({protocol:'HTTP',supports_udp:false})],total:'1',page:1,page_size:20,observed_at:at},q));
  assert.throws(()=>readPage({items:[item(),item()],total:'2',page:1,page_size:20,observed_at:at},{page:1,page_size:20}));
  assert.equal(queryProblem({page:1,page_size:20,country_code:'sg'}),'国家代码应为两位大写字母，例如 SG');
});
test('预览：合计必须对得上，订阅项不得带回完整地址',()=>{
  const rows=[{line:1,source:'socks5://al***@203.0.113.10:1080',protocol:'SOCKS5',supports_udp:false},{line:2,source:'（8 字符）',supports_udp:false,error:'无法识别的格式'}];
  assert.equal(readPreview({rows,summary:{total:2,valid:1,invalid:1,duplicate:0,existing:0}}).summary.valid,1);
  assert.throws(()=>readPreview({rows,summary:{total:3,valid:1,invalid:1,duplicate:0,existing:0}}));
  const sub={id:'s1',name:'A',url_hint:'sub.example.com',default_protocol:'SOCKS5',country_code:'SG',region:'',min_tier:1,status:'ACTIVE',last_fetched_at:null,last_result:''};
  assert.equal(readSubscription(sub).url_hint,'sub.example.com');
  assert.throws(()=>readSubscription({...sub,url:'https://sub.example.com/token'}));
  assert.throws(()=>readImport({created:1,updated:0,skipped:0,failed:0,disabled:0,ids:['12'],rows:[]}));
});
test('导入选项：HTTP 类不能声称 UDP，国家代码大写两位',()=>{
  assert.equal(optionsProblem(opts),'');
  assert.match(optionsProblem({...opts,default_protocol:'HTTP',default_udp:true}),/UDP/);
  assert.match(optionsProblem({...opts,country_code:'Singapore'}),/国家代码/);
  assert.match(optionsProblem({...opts,min_tier:0}),/等级/);
});
test('真实接口：列表按管理端约定直接读 JSON；写入携带 UUIDv4 操作编号',async t=>{
  const calls=[];
  fetchMock(t,async(url,init)=>{calls.push({url,init});
    if(url.startsWith('/admin/v1/egress?'))return new Response(JSON.stringify({items:[item()],total:'1',page:1,page_size:20,observed_at:at}));
    return new Response(JSON.stringify({created:1,updated:0,skipped:0,failed:0,disabled:0,ids:['e13'],rows:[]}),{status:201});});
  const page=await liveEgressSource.list({page:1,page_size:20});
  assert.equal(page.total,'1');
  const op=crypto.randomUUID();assert.ok(validOperationId(op));
  const r=await liveEgressSource.importNow(op,'203.0.113.10:1080:u:p',opts);
  assert.deepEqual(r.ids,['e13']);
  const sent=JSON.parse(calls[1].init.body);
  assert.equal(sent.operation_id,op);assert.equal(sent.text,'203.0.113.10:1080:u:p');
  await assert.rejects(()=>liveEgressSource.importNow('not-uuid','x',opts));
});
test('演示数据源：预览不写入、内网与重复逐行报错、同一操作编号不重复导入',async()=>{
  const src=makePreviewEgress({id:'1',username:'demo',permissions:[],csrf_token:''});
  const before=(await src.list({page:1,page_size:50})).total;
  const text='203.0.113.50:1080:a:b\n203.0.113.50:1080:a:b\n10.0.0.1:1080\nnot a line\ntuic://x@h.example.com:443';
  const p=await src.preview(text,opts);
  assert.deepEqual(p.summary,{total:5,valid:1,invalid:3,duplicate:1,existing:0});
  assert.equal((await src.list({page:1,page_size:50})).total,before);
  const op=crypto.randomUUID();
  const a=await src.importNow(op,text,opts),b=await src.importNow(op,text,opts);
  assert.equal(a.created,1);assert.deepEqual(a,b);
  assert.equal((await src.list({page:1,page_size:50})).total,String(Number(before)+1));
});
