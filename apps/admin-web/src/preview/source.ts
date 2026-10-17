import type {AdminSession} from '../api/client';
import type {Approval, AuditEntry, Page, ReviewSource} from '../api/review';
import {VersionWriteError, type AppVersion, type VersionSource} from '../api/versions.ts';
import {decisionBlock} from '../pages/review-model.ts';
import {draftProblem, normalizeDraft, versionBlock} from '../pages/versions-model.ts';

const hash = (seed:number) => seed.toString(16).padStart(64,'0');
const pageOf = <T,>(items:T[],page:number,size:number):Page<T> => ({items:structuredClone(items.slice((page-1)*size,page*size)),page,page_size:size,total:String(items.length)});
export type PreviewSource = ReviewSource & {versions:VersionSource;recordDeviceRevocation:(deviceId:string)=>void};
export function makePreviewSource(session:AdminSession):PreviewSource {
  const start=Date.now(); let revision=9500,versionSequence=308n;
  const row=(id:string,author:string,platform:string,status:Approval['status']='PENDING'):Approval=>({id,action_code:'app_versions.publish',target_type:'app_version',target_id:String(Number(id)-7000),status,reason:platform==='ANDROID'?'Android 更新：连接体验优化，修复设备列表刷新问题':'iOS 更新：账号安全提示与帮助文案',requested_by:author,decided_by:status==='EXECUTED'?'26':null,created_at:new Date(start-55*60000-Number(id)%10*60000).toISOString(),expires_at:new Date(start+23*3600000).toISOString(),decided_at:status==='EXECUTED'?new Date(start-30*60000).toISOString():null,executed_at:status==='EXECUTED'?new Date(start-30*60000).toISOString():null,payload_hash:hash(Number(id)),executor_supported:true,snapshot:{platform,version_name:platform==='ANDROID'?'1.2.0':'1.1.2',version_code:platform==='ANDROID'?'120':'112',min_supported_code:'100',status:'DRAFT',release_notes:'示例资料，用于演示双人审批；当前没有真实安装包。',download_url:'https://downloads.example.com/yuhai-demo.apk',file_sha256:hash(120),cert_sha256:platform==='ANDROID'?hash(121):null}});
  const approvals=[row('7306','26','ANDROID'),row('7305','35','IOS'),row('7304','18','ANDROID'),{...row('7303','26','ANDROID'),action_code:'invite.reward',target_type:'invite_rule',reason:'奖励规则示例：当前未接入执行，不能通过审批启用',executor_supported:false,snapshot:undefined},row('7302','35','ANDROID','EXECUTED'),{...row('7301','18','IOS','REJECTED'),decision_note:'请补充安装包校验信息后重新提交',decided_by:'26',decided_at:new Date(start-40*60000).toISOString()}];
  let approvalSequence=approvals.reduce((maximum,a)=>BigInt(a.id)>maximum?BigInt(a.id):maximum,0n)+1n;
  const versions:AppVersion[]=approvals.filter(a=>a.executor_supported).map(a=>{
    const platform=a.snapshot!.platform as AppVersion['platform'];
    const versionCode=platform==='ANDROID'?({'306':'120','304':'119','302':'118'}[a.target_id!]!):a.target_id==='305'?'112':'111';
    a.snapshot={...a.snapshot,version_code:versionCode,id:a.target_id,version_name:platform==='ANDROID'?`1.${versionCode==='120'?'2.0':`1.${versionCode==='119'?'9':'8'}`}`:`1.1.${versionCode==='112'?'2':'1'}`};
    return {id:a.target_id!,platform,version_name:a.snapshot.version_name!,version_code:versionCode,min_supported_code:'100',download_url:a.snapshot.download_url!,file_sha256:a.snapshot.file_sha256!,cert_sha256:a.snapshot.cert_sha256??null,release_notes:a.snapshot.release_notes!,status:a.status==='EXECUTED'?'PUBLISHED':'DRAFT',published_at:a.status==='EXECUTED'?a.executed_at!:null,approval_id:a.status==='EXECUTED'?a.id:null,created_at:a.created_at,snapshot_hash:a.payload_hash,pending_approval_id:a.status==='PENDING'?a.id:null,can_edit:a.status==='REJECTED'};
  });
  versions.unshift({id:'307',platform:'ANDROID',version_name:'1.3.0',version_code:'130',min_supported_code:'100',download_url:'https://downloads.example.com/yuhai-1.3.0-demo.apk',file_sha256:hash(130),cert_sha256:hash(121),release_notes:'示例草稿：优化版本提示与设备管理体验。此地址仅供界面演示，不提供真实安装包。',status:'DRAFT',published_at:null,approval_id:null,created_at:new Date(start-10*60000).toISOString(),snapshot_hash:hash(307),pending_approval_id:null,can_edit:true});
  // Seed only events backed by the actual examples above. Do not manufacture
  // approval IDs: future in-memory requests must never inherit unrelated logs.
  const seedEvents:Pick<AuditEntry,'admin_user_id'|'action_code'|'target_type'|'target_id'|'approval_id'|'created_at'>[]=[
    ...versions.filter(v=>['306','305','304','302'].includes(v.id)).map(v=>({admin_user_id:approvals.find(a=>a.target_type==='app_version'&&a.target_id===v.id)!.requested_by,action_code:'app_versions.draft',target_type:'app_version',target_id:v.id,approval_id:null,created_at:v.created_at})),
    ...approvals.map(a=>({admin_user_id:a.requested_by,action_code:'approvals.create',target_type:a.target_type,target_id:a.target_id,approval_id:a.id,created_at:a.created_at})),
    ...approvals.filter(a=>a.status==='EXECUTED'||a.status==='REJECTED').map(a=>({admin_user_id:a.decided_by!,action_code:a.status==='EXECUTED'?'approvals.approve':'approvals.reject',target_type:a.target_type,target_id:a.target_id,approval_id:a.id,created_at:a.decided_at!})),
  ];
  const logs:AuditEntry[]=seedEvents.sort((a,b)=>Date.parse(a.created_at)-Date.parse(b.created_at)).map<AuditEntry>((event,i)=>({ ...event,id:String(9001+i),request_id:`demo-seed-${9001+i}`,result:'SUCCESS',step_up_verified:true,prev_hash:hash(i===0?0:9000+i),row_hash:hash(9001+i)})).reverse();
  const operations=new Map<string,{body:string;id:string;snapshot:string}>();
  const requirePermission=(permission:string)=>{if(!session.permissions.includes(permission))throw new Error('当前演示账号没有此项权限');};
  const liveVersion=(id:string)=>{const value=versions.find(v=>v.id===id);if(!value)throw new Error('版本不存在');return value;};
  function viewVersion(v:AppVersion):AppVersion {
    const pending=approvals.find(a=>a.action_code==='app_versions.publish'&&a.target_id===v.id&&a.status==='PENDING'&&Date.parse(a.expires_at)>Date.now());
    return structuredClone({...v,pending_approval_id:pending?.id??null,can_edit:v.status==='DRAFT'&&!pending&&v.published_at===null&&v.approval_id===null});
  }
  function log(action:string,targetID:string,approvalID:string|null=null,targetType:string|null='app_version') {
    const id=String(BigInt(logs[0].id)+1n);
    logs.unshift({id,action_code:action,admin_user_id:session.id,target_type:targetType,target_id:targetID,approval_id:approvalID,request_id:`demo-${id}`,result:'SUCCESS',step_up_verified:true,created_at:new Date().toISOString(),prev_hash:logs[0].row_hash,row_hash:hash(++revision)});
  }
  const snapshot=(v:AppVersion):Record<string,string|null>=>({id:v.id,platform:v.platform,version_name:v.version_name,version_code:v.version_code,min_supported_code:v.min_supported_code,download_url:v.download_url,file_sha256:v.file_sha256,cert_sha256:v.cert_sha256,release_notes:v.release_notes,status:v.status,published_at:v.published_at,approval_id:v.approval_id});
  function confirmOnce<T>(operation:()=>T) {
    let attempted=false;
    return {confirm:async(code:string)=>{if(attempted)throw new VersionWriteError(new Error('请重新查询详情'),false);attempted=true;if(code!=='123456')throw new VersionWriteError(new Error('演示验证码为123456'),false);return operation();}};
  }
  const versionSource:VersionSource={
    list:async q=>{requirePermission('app_versions.read');if(q.version_code&&!q.platform)throw new Error('请选择平台');return pageOf(versions.filter(v=>(!q.platform||v.platform===q.platform)&&(!q.status||v.status===q.status)&&(!q.version_code||v.version_code===q.version_code)).map(viewVersion).sort((a,b)=>Date.parse(b.created_at)-Date.parse(a.created_at)||(BigInt(a.id)>BigInt(b.id)?-1:1)),q.page,q.page_size);},
    get:async id=>{requirePermission('app_versions.read');return viewVersion(liveVersion(id));},
    find:async(platform,code)=>{requirePermission('app_versions.read');const value=versions.find(v=>v.platform===platform&&v.version_code===code);return value?viewVersion(value):null;},
    prepareSave:async(input,target)=>{
      const payload=structuredClone(input),base=target?structuredClone(target):undefined,fields=normalizeDraft(payload);
      return confirmOnce(()=>{
        requirePermission('app_versions.draft');const problem=draftProblem(fields);if(problem)throw new Error(problem);
        const body=JSON.stringify({target:base?.id??null,...payload}),previousOperation=operations.get(payload.operation_id);
        if(previousOperation){if(previousOperation.body!==body||liveVersion(previousOperation.id).snapshot_hash!==previousOperation.snapshot)throw new Error('此操作内容已经变化，请重新查询');return viewVersion(liveVersion(previousOperation.id));}
        const conflict=versions.some(v=>v.platform===fields.platform&&v.version_code===fields.version_code&&v.id!==base?.id);
        if(conflict)throw new Error('这个平台的版本编号已存在');
        let value:AppVersion;
        if(base){value=liveVersion(base.id);const blocked=versionBlock(viewVersion(value),session,'edit');if(blocked)throw new Error(blocked);if(value.snapshot_hash!==base.snapshot_hash)throw new Error('版本资料已变化，请重新查询后编辑');Object.assign(value,fields,{snapshot_hash:hash(++revision)});}
        else{value={...fields,id:String(versionSequence++),status:'DRAFT',published_at:null,approval_id:null,created_at:new Date().toISOString(),snapshot_hash:hash(++revision),pending_approval_id:null,can_edit:true};versions.unshift(value);}
        operations.set(payload.operation_id,{body,id:value.id,snapshot:value.snapshot_hash});log('app_versions.draft',value.id);return viewVersion(value);
      });
    },
    preparePublish:async(version,reason)=>{
      const expected=structuredClone(version),text=reason;
      return confirmOnce(()=>{
        requirePermission('app_versions.publish');const value=liveVersion(expected.id),blocked=versionBlock(viewVersion(value),session,'publish');if(blocked)throw new Error(blocked);
        if(value.snapshot_hash!==expected.snapshot_hash)throw new Error('版本资料已变化，请重新查询');if(!text.trim()||[...text].length>500)throw new Error('请填写发布理由');
        const approval:Approval={id:String(approvalSequence++),action_code:'app_versions.publish',target_type:'app_version',target_id:value.id,status:'PENDING',reason:text,requested_by:session.id,created_at:new Date().toISOString(),expires_at:new Date(Date.now()+24*3600000).toISOString(),decided_by:null,decided_at:null,executed_at:null,decision_note:null,payload_hash:value.snapshot_hash,executor_supported:true,snapshot:snapshot(value)};
        approvals.unshift(approval);log('approvals.create',value.id,approval.id);return structuredClone(approval);
      });
    },
  };
  return {
    versions:versionSource,
    recordDeviceRevocation:deviceId=>log('devices.revoke',deviceId,null,'device'),
    listApprovals:async q=>pageOf(approvals.filter(a=>(q.scope==='all'||q.scope==='mine'&&a.requested_by===session.id||q.scope==='pending'&&a.requested_by!==session.id&&a.status==='PENDING')&&(!q.status||a.status===q.status)),q.page,q.page_size),
    getApproval:async id=>{const a=approvals.find(a=>a.id===id);if(!a)throw new Error('审批不存在');return structuredClone(a);},
    prepareDecision:async(id,decision,note)=>{let attempted=false;return {confirm:async code=>{
      if(attempted)throw new Error('请重新查询详情');attempted=true;if(code!=='123456')throw new Error('演示验证码为123456');const a=approvals.find(a=>a.id===id);if(!a)throw new Error('审批不存在');const reason=decisionBlock(a,session,decision);if(reason)throw new Error(reason);if(decision==='reject'&&!note.trim())throw new Error('请填写拒绝理由');
      if(decision==='approve'){const version=liveVersion(a.target_id!);if(version.status!=='DRAFT'||version.snapshot_hash!==a.payload_hash)throw new Error('版本资料已变化');Object.assign(version,{status:'PUBLISHED',published_at:new Date().toISOString(),approval_id:a.id,snapshot_hash:hash(++revision)});}
      a.status=decision==='approve'?'EXECUTED':'REJECTED';a.decided_by=session.id;a.decided_at=new Date().toISOString();a.decision_note=decision==='reject'?note:null;if(decision==='approve')a.executed_at=a.decided_at;log(`approvals.${decision}`,a.target_id!,a.id,a.target_type);return structuredClone(a);
    }};},
    listAudit:async q=>pageOf(logs.filter(x=>!q.result||x.result===q.result),q.page,q.page_size),
    verify:async(from,to)=>{const items=logs.filter(x=>BigInt(x.id)>=BigInt(from)&&BigInt(x.id)<=BigInt(to));const valid=items.some(x=>x.id===from)&&items.some(x=>x.id===to);return {valid,from_id:from,to_id:to,checked:items.length,head_id:logs[0].id,head_hash:logs[0].row_hash,reason:valid?'仅演示校验流程，示例校验码不代表真实审计链':'示例记录中不存在所选起止编号',external_anchor:false};},
  };
}
