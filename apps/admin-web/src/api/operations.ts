import {adminApi,prepareProtectedRequest,type AdminSession} from './client.ts';
export type Row=Record<string,unknown>;
export type Column={key:string;label:string};
export type Module={title:string;path:string;permission:string;columns:Column[];filters?:Column[];paged?:boolean;note?:string;defaults?:Record<string,string>};
const cols=(pairs:string[][])=>pairs.map(([key,label])=>({key,label}));
export const modules:Record<string,Module>={
 devices:{title:'设备总览',path:'/devices',permission:'users.read',paged:true,filters:cols([['user_id','用户编号'],['status','状态'],['platform','平台']]),columns:cols([['id','设备编号'],['user_id','用户编号'],['platform','平台'],['display_name','设备名称'],['status','状态'],['last_seen_at','最近活动']]),note:'设备有效不代表当前在线。'},
 plans:{title:'套餐管理',path:'/plans',permission:'plans.read',filters:cols([['status','状态']]),columns:cols([['id','编号'],['code','套餐代码'],['name','名称'],['plan_type','类型'],['price','价格（USDT）'],['tier','等级'],['duration_days','时长（天）'],['period_quota_bytes','周期流量（字节）'],['device_limit','设备数'],['status','状态'],['version','版本']])},
 orders:{title:'订单',path:'/orders',permission:'finance.read',paged:true,filters:cols([['order_no','订单号'],['user_id','用户编号'],['status','状态']]),columns:cols([['order_no','订单号'],['user_id','用户编号'],['order_type','类型'],['pay_method','支付方式'],['pay_amount','应付金额'],['currency','币种'],['status','状态'],['created_at','创建时间']])},
 'chain-transfers':{title:'链上记录',path:'/chain-transfers',permission:'finance.read',paged:true,filters:cols([['tx_hash','交易哈希'],['match_status','匹配状态']]),columns:cols([['tx_hash','交易哈希'],['from_address','付款地址'],['to_address','收款地址'],['amount','金额'],['confirm_status','链上状态'],['match_status','匹配状态'],['order_no','订单号']])},
 'deposit-exceptions':{title:'异常到账',path:'/deposit-exceptions',permission:'finance.read',paged:true,filters:cols([['status','状态']]),columns:cols([['id','编号'],['reason','原因'],['status','状态'],['chain_transfer.amount','金额'],['chain_transfer.tx_hash','交易哈希'],['suggested_order_no','疑似订单'],['resolution_note','处理说明']]),note:'疑似订单仅作参考，处置申请需另一位管理员审批。'},
 'receive-addresses':{title:'收款地址',path:'/receive-addresses',permission:'finance.read',note:'新地址由管理员登记，先暂停，确认后再启用。系统不保存收款地址的私钥。',columns:cols([['id','编号'],['network','网络'],['address','地址'],['label','备注'],['status','状态'],['valid_until','有效期']])},
 traffic:{title:'流量统计',path:'/traffic/stats',permission:'traffic.read',paged:true,defaults:{dim:'day'},filters:cols([['dim','维度：day / user / device / node'],['from','起始日期'],['to','结束日期']]),columns:cols([['stat_date','日期'],['user_id','用户编号'],['device_id','设备编号'],['node_id','节点编号'],['upload_bytes','上传字节'],['download_bytes','下载字节'],['total_bytes','合计字节']]),note:'统计已接收入账的节点上报，并非即时流量。'},
 invites:{title:'邀请关系',path:'/invite-relations',permission:'invite.read',paged:true,filters:cols([['inviter_user_id','邀请人编号'],['invitee_user_id','被邀请人编号']]),columns:cols([['inviter_user_id','邀请人'],['invitee_user_id','被邀请人'],['invite_code','邀请码'],['bind_method','绑定方式'],['created_at','时间']])},
 rewards:{title:'邀请奖励',path:'/invite-rewards',permission:'invite.read',paged:true,filters:cols([['status','状态'],['beneficiary_user_id','受益人编号']]),columns:cols([['id','编号'],['beneficiary_user_id','受益人'],['reward_type','类型'],['reward_value','数量'],['value_unit','单位'],['status','状态'],['release_at','预计释放'],['cancel_reason','说明']])},
 blacklist:{title:'邀请风控名单',path:'/blacklist',permission:'risk.read',paged:true,filters:cols([['status','状态'],['target_type','对象类型']]),columns:cols([['id','编号'],['target_type','对象类型'],['effects','作用范围'],['reason','原因'],['status','状态'],['expires_at','有效期']]),note:'名单作用于邀请绑定与奖励；账户登录和连接状态在用户管理中处理。'},
 admins:{title:'管理员',path:'/admins',permission:'admins.read',paged:true,filters:cols([['username','用户名'],['status','状态']]),columns:cols([['id','编号'],['username','用户名'],['status','状态'],['role_ids','角色编号'],['last_login_at','最近登录']]),note:'新建管理员及角色变更需要双人审批。'},
 exports:{title:'数据导出',path:'/exports',permission:'exports.read',paged:true,columns:cols([['id','编号'],['kind','数据集'],['status','状态'],['row_count','行数'],['created_at','创建时间'],['expires_at','过期时间']])},
 'scan-cursors':{title:'扫描游标',path:'/scan-cursors',permission:'finance.read',columns:cols([['network','网络'],['address','地址'],['last_block_number','最近确认区块'],['updated_at','更新时间']])},
};
export function cell(row:Row,path:string):unknown{return path.split('.').reduce<unknown>((v,k)=>v&&typeof v==='object'&&!Array.isArray(v)?(v as Row)[k]:undefined,row);}
export function display(value:unknown):string {if(value===undefined||value===null)return '—';if(typeof value==='string'||typeof value==='boolean')return String(value);if(typeof value==='number'&&Number.isSafeInteger(value))return String(value);if(Array.isArray(value)&&value.every(v=>typeof v==='string'))return value.join('、');return '—';}
export function modulePath(module:Module,filters:Record<string,string>,page:number){const query=new URLSearchParams();for(const f of module.filters??[]){const v=filters[f.key]?.trim()||module.defaults?.[f.key];if(v)query.set(f.key,v);}if(module.paged){query.set('page',String(page));query.set('page_size','20');}return module.path+(query.size?'?'+query.toString():'');}
export async function readModule(module:Module,filters:Record<string,string>,page:number,signal:AbortSignal){const raw=await adminApi.get<unknown>(modulePath(module,filters,page),signal);if(!raw||typeof raw!=='object'||Array.isArray(raw))throw new Error('返回数据不完整');const data=raw as Row;if(!Array.isArray(data.items)||data.items.some(v=>!v||typeof v!=='object'||Array.isArray(v)))throw new Error('列表数据不完整');if(data.total!==undefined&&(typeof data.total!=='string'||!/^\d+$/.test(data.total)))throw new Error('总数格式错误');return {items:data.items as Row[],total:data.total===undefined?String(data.items.length):data.total as string,raw:data};}
export type ProtectedAction={title:string;method:'POST'|'PATCH'|'PUT'|'DELETE';path:string;permission:string;body:Row;approval?:boolean};
function immutable<T>(value:T):T {if(value&&typeof value==='object'){for(const child of Object.values(value as Row))immutable(child);Object.freeze(value);}return value;}
export async function prepareAction(action:ProtectedAction,session:AdminSession){
 if(!session.permissions.includes(action.permission))throw new Error('没有此操作权限');
 const frozen=immutable(structuredClone(action.body));
 const request=await prepareProtectedRequest<unknown>(action.method,action.path,frozen);
 let attempted=false,confirming=false;
 return {body:frozen,get attempted(){return attempted;},confirm:async(code:string)=>{
  if(attempted||confirming)throw new Error('请求已提交或正在验证，请勿重复操作');
  if(!/^\d{6}$/.test(code))throw new Error('请输入6位动态码');
  confirming=true;
  try {
   const proof=await adminApi.stepUp({action_code:action.permission,payload_hash:request.payloadHash,totp_code:code});
   attempted=true;
   const response=await request.send(proof.step_up_token);
   const row=response&&typeof response==='object'&&!Array.isArray(response)?response as Row:null;
   if(action.approval&&(!row||typeof row.approval_id!=='string'||!/^\d+$/.test(row.approval_id)))throw new Error('审批回执不完整，结果待核实');
   if(['plans.create','plans.update','finance.address_status','invite.reward_review'].includes(action.permission)){
    const expected=action.path.match(/^\/(?:plans|receive-addresses|invite-rewards)\/(\d+)/)?.[1];
    if(!row||typeof row.id!=='string'||!/^\d+$/.test(row.id)||(expected&&row.id!==expected))throw new Error('操作回执不完整，结果待核实');
   }
   return response;
  }finally{confirming=false;}
 }};
}
