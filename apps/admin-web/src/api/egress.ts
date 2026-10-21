import {adminApi} from './client.ts';

// 出口（外购代理 / 自建多协议节点）后台接口：列表、导入预览、导入、修改、订阅。
// 对应 server/internal/admin/egress*.go 与 api/openapi/admin.yaml。凭据永远不会出现在任何响应里，
// 这里只做形状核对：返回不完整就报错，不把半份数据当真。

export const egressProtocols=['SOCKS5','HTTP','HTTPS','SHADOWSOCKS','TROJAN','VLESS','VMESS','HYSTERIA2'] as const;
export type EgressProtocol=typeof egressProtocols[number];
export type EgressStatus='ENABLED'|'DISABLED';
export const protocolLabels:Record<string,string>={SOCKS5:'SOCKS5',HTTP:'HTTP 代理',HTTPS:'HTTPS 代理',SHADOWSOCKS:'Shadowsocks',TROJAN:'Trojan',VLESS:'VLESS',VMESS:'VMess',HYSTERIA2:'Hysteria2',TUIC:'TUIC'};

export type EgressItem={
  id:string;name:string;protocol:string;host:string;port:number;country_code:string;city:string;region:string;kind:string;
  supports_udp:boolean;capacity_users:number;identity_hint:string;params:Record<string,string>;min_tier:number;status:EgressStatus;sort_order:number;
  source:string;subscription_id:string|null;created_at:string;updated_at:string;
};
export type EgressPage={items:EgressItem[];total:string;page:number;page_size:number;observed_at:string};
export type EgressQuery={page:number;page_size:number;status?:EgressStatus;protocol?:string;country_code?:string;search?:string};
export type ImportOptions={default_protocol:'SOCKS5'|'HTTP'|'HTTPS';default_udp:boolean;country_code:string;city:string;region:string;name_prefix:string;min_tier:number;enable:boolean;update_existing:boolean};
export type PreviewRow={line:number;source:string;protocol?:string;host?:string;port?:number;name?:string;country_code?:string;supports_udp:boolean;error?:string;duplicate?:boolean;existing_id?:string};
export type PreviewResult={rows:PreviewRow[];summary:{total:number;valid:number;invalid:number;duplicate:number;existing:number}};
export type ImportResult={created:number;updated:number;skipped:number;failed:number;disabled:number;ids:string[];rows:{line:number;source:string;error?:string}[]};
export type EgressUpdate={name?:string;status?:EgressStatus;country_code?:string;city?:string;region?:string;min_tier?:number;sort_order?:number;supports_udp?:boolean;capacity_users?:number};
export type Subscription={id:string;name:string;url_hint:string;default_protocol:string;country_code:string;region:string;min_tier:number;status:string;last_fetched_at:string|null;last_result:string;endpoint_count?:number};
export type SubscriptionInput={name:string;url:string;default_protocol:'SOCKS5'|'HTTP'|'HTTPS';country_code:string;region:string;min_tier:number};

export type EgressSource={
  list(q:EgressQuery,signal?:AbortSignal):Promise<EgressPage>;
  preview(text:string,o:ImportOptions):Promise<PreviewResult>;
  importNow(operationId:string,text:string,o:ImportOptions):Promise<ImportResult>;
  update(operationId:string,id:string,change:EgressUpdate):Promise<EgressItem>;
  subscriptions(signal?:AbortSignal):Promise<Subscription[]>;
  previewSubscription(input:SubscriptionInput):Promise<PreviewResult>;
  createSubscription(operationId:string,input:SubscriptionInput):Promise<Subscription>;
  refreshSubscription(operationId:string,id:string,enable:boolean):Promise<ImportResult>;
};

const invalid=()=>new Error('出口资料返回不完整，请重新查询');
const object=(v:unknown):v is Record<string,unknown>=>typeof v==='object'&&v!==null&&!Array.isArray(v);
const int=(v:unknown,min=0,max=2147483647):v is number=>typeof v==='number'&&Number.isInteger(v)&&v>=min&&v<=max;
const str=(v:unknown,max=512):v is string=>typeof v==='string'&&[...v].length<=max;
export const validEgressId=(v:unknown):v is string=>typeof v==='string'&&/^e[1-9][0-9]{0,18}$/.test(v);
export const validSubscriptionId=(v:unknown):v is string=>typeof v==='string'&&/^s[1-9][0-9]{0,18}$/.test(v);
export const validOperationId=(v:string)=>/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(v);
// 列表与预览里的「身份提示」只允许脱敏形态：出现疑似完整密码/UUID 就拒绝显示。
const masked=(v:unknown)=>str(v,16)&&!/[0-9a-f]{8}-[0-9a-f]{4}-/i.test(v as string);

export function queryProblem(q:EgressQuery):string{
  if(!int(q.page,1,10000)||!int(q.page_size,1,100))return '分页参数无效';
  if(q.status&&!['ENABLED','DISABLED'].includes(q.status))return '状态筛选无效';
  if(q.protocol&&!Object.keys(protocolLabels).includes(q.protocol))return '协议筛选无效';
  if(q.country_code&&!/^[A-Z]{2}$/.test(q.country_code))return '国家代码应为两位大写字母，例如 SG';
  if(q.search!==undefined&&(!q.search.trim()||[...q.search].length>64))return '搜索词应为 1–64 字';
  return '';
}
export function optionsProblem(o:ImportOptions):string{
  if(!['SOCKS5','HTTP','HTTPS'].includes(o.default_protocol))return '默认协议只能是 SOCKS5、HTTP 或 HTTPS';
  if(o.country_code&&!/^[A-Z]{2}$/.test(o.country_code))return '国家代码应为两位大写字母，例如 SG、US、JP';
  if(!int(o.min_tier,1,9))return '最低套餐等级应在 1–9';
  if([...o.city].length>64||[...o.region].length>32||[...o.name_prefix].length>48)return '城市、地区或名称前缀过长';
  if(o.default_protocol!=='SOCKS5'&&o.default_udp)return 'HTTP/HTTPS 代理不支持 UDP';
  return '';
}

export function readItem(v:unknown):EgressItem{
  if(!object(v)||!validEgressId(v.id)||!str(v.name,64)||!str(v.protocol,32)||!str(v.host,253)||!int(v.port,1,65535)||!str(v.country_code,2)||
    !str(v.city,64)||!str(v.region,32)||typeof v.supports_udp!=='boolean'||!int(v.capacity_users,0,1000000)||!masked(v.identity_hint)||!object(v.params)||!int(v.min_tier,1,9)||
    !['ENABLED','DISABLED'].includes(v.status as string)||!int(v.sort_order,-2147483648)||!str(v.source,32)||(v.subscription_id!==null&&!str(v.subscription_id,24))||
    !str(v.created_at,40)||!str(v.updated_at,40))throw invalid();
  // HTTP 类出口声称支持 UDP：数据不一致，拒绝显示而不是照搬。
  if((v.protocol==='HTTP'||v.protocol==='HTTPS')&&v.supports_udp)throw invalid();
  for(const [k,val] of Object.entries(v.params)){if(!str(k,32)||!str(val,512)||/pass|password|uuid|secret/i.test(k))throw invalid();}
  return structuredClone(v) as EgressItem;
}
export function readPage(v:unknown,q:EgressQuery):EgressPage{
  if(!object(v)||!Array.isArray(v.items)||typeof v.total!=='string'||!/^(0|[1-9][0-9]{0,18})$/.test(v.total)||v.page!==q.page||v.page_size!==q.page_size||!str(v.observed_at,40))throw invalid();
  const items=v.items.map(readItem);
  if(items.length>q.page_size||new Set(items.map(i=>i.id)).size!==items.length)throw invalid();
  if(items.some(i=>q.status&&i.status!==q.status||q.protocol&&i.protocol!==q.protocol||q.country_code&&i.country_code!==q.country_code))throw invalid();
  return {items,total:v.total,page:q.page,page_size:q.page_size,observed_at:v.observed_at};
}
export function readPreview(v:unknown):PreviewResult{
  if(!object(v)||!Array.isArray(v.rows)||!object(v.summary))throw invalid();
  const s=v.summary;
  if(!int(s.total)||!int(s.valid)||!int(s.invalid)||!int(s.duplicate)||!int(s.existing)||s.valid+s.invalid+s.duplicate!==s.total||v.rows.length!==s.total)throw invalid();
  const rows=v.rows.map((r:unknown)=>{
    if(!object(r)||!int(r.line,1)||!str(r.source,120)||typeof r.supports_udp!=='boolean'||(r.error!==undefined&&!str(r.error,200)))throw invalid();
    return structuredClone(r) as PreviewRow;
  });
  return {rows,summary:structuredClone(s) as PreviewResult['summary']};
}
export function readImport(v:unknown):ImportResult{
  if(!object(v)||!int(v.created)||!int(v.updated)||!int(v.skipped)||!int(v.failed)||!int(v.disabled)||!Array.isArray(v.ids)||!v.ids.every(validEgressId)||!Array.isArray(v.rows))throw invalid();
  return structuredClone(v) as ImportResult;
}
export function readSubscription(v:unknown):Subscription{
  if(!object(v)||!validSubscriptionId(v.id)||!str(v.name,64)||!str(v.url_hint,253)||!str(v.default_protocol,16)||!str(v.country_code,2)||!str(v.region,32)||!int(v.min_tier,1,9)||!str(v.status,16)||(v.last_fetched_at!==null&&!str(v.last_fetched_at,40))||!str(v.last_result,300))throw invalid();
  // 订阅地址本身（可能带令牌）永远不回到浏览器：出现 url 字段即视为异常。
  if('url' in v)throw invalid();
  return structuredClone(v) as Subscription;
}

const body=(text:string,o:ImportOptions)=>({text,...o});
export const liveEgressSource:EgressSource={
  list:async(q,signal)=>{
    const p=queryProblem(q);if(p)throw new Error(p);
    const params=new URLSearchParams({page:String(q.page),page_size:String(q.page_size)});
    for(const k of ['status','protocol','country_code','search'] as const)if(q[k])params.set(k,q[k]!);
    return readPage(await adminApi.get(`/egress?${params}`,signal),q);
  },
  preview:async(text,o)=>{const p=optionsProblem(o);if(p)throw new Error(p);return readPreview(await adminApi.post('/egress/import/preview',body(text,o)));},
  importNow:async(op,text,o)=>{if(!validOperationId(op))throw new Error('操作编号无效');return readImport(await adminApi.post('/egress/import',{operation_id:op,...body(text,o)}));},
  update:async(op,id,change)=>{if(!validOperationId(op)||!validEgressId(id))throw new Error('操作编号或出口编号无效');return readItem(await adminApi.patch(`/egress/${id}`,{operation_id:op,...change}));},
  subscriptions:async signal=>{const r=await adminApi.get<{items:unknown[]}>('/egress-subscriptions',signal);if(!object(r)||!Array.isArray(r.items))throw invalid();return r.items.map(readSubscription);},
  previewSubscription:async input=>readPreview(await adminApi.post('/egress-subscriptions/preview',input)),
  createSubscription:async(op,input)=>readSubscription(await adminApi.post('/egress-subscriptions',{operation_id:op,...input})),
  refreshSubscription:async(op,id,enable)=>{if(!validSubscriptionId(id))throw new Error('订阅编号无效');return readImport(await adminApi.post(`/egress-subscriptions/${id}/refresh`,{operation_id:op,enable}));},
};
