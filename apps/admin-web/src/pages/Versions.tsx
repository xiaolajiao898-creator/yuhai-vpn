import {useEffect, useRef, useState} from 'react';
import {Link} from 'react-router-dom';
import {Alert, Button, Descriptions, Drawer, Empty, Input, Modal, Select, Spin, Table, Tag} from 'antd';
import {AndroidOutlined, AppleOutlined, EditOutlined, FileProtectOutlined, PlusOutlined, ReloadOutlined, SendOutlined} from '@ant-design/icons';
import type {AdminSession} from '../api/client';
import type {Approval, Page, ReviewSource} from '../api/review';
import {sameVersionFields, VersionWriteError, type AppVersion, type PreparedWrite, type SaveDraft, type VersionFields, type VersionQuery, type VersionSource} from '../api/versions';
import {boundedTotal} from './review-model';
import {draftProblem, emptyDraft, normalizeDraft, platformLabels, versionBlock, versionStatusLabels} from './versions-model';

type SaveConfirmation = {kind:'save'; payload:SaveDraft; previous?:AppVersion; prepared:PreparedWrite<AppVersion>};
type PublishConfirmation = {kind:'publish'; version:AppVersion; reason:string; prepared:PreparedWrite<Approval>};
type Confirmation = SaveConfirmation | PublishConfirmation;
type Recovery = {attempt:Confirmation; error:string};
const issue = (error:unknown) => error instanceof Error ? error.message : '暂时无法读取资料，请重试';
const time = (value:string|null) => value ? new Date(value).toLocaleString('zh-CN',{timeZone:'Asia/Shanghai',hour12:false}) : '—';
const statusColor = (v:AppVersion) => v.pending_approval_id ? 'gold' : v.status==='PUBLISHED' ? 'green' : v.status==='WITHDRAWN' ? 'default' : 'blue';
const statusText = (v:AppVersion) => v.pending_approval_id ? '待审批' : versionStatusLabels[v.status];
const approvalLink = (id:string) => `/approvals?approval=${encodeURIComponent(id)}`;

export function VersionsPage({source,reviews,session,demo=false}:{source:VersionSource;reviews:ReviewSource;session:AdminSession;demo?:boolean}) {
  const [query,setQuery] = useState<VersionQuery>({page:1,page_size:20});
  const [list,setList] = useState<Page<AppVersion>>();
  const [listBusy,setListBusy] = useState(false), [listError,setListError] = useState('');
  const [refresh,setRefresh] = useState(0), [detailRefresh,setDetailRefresh] = useState(0);
  const [selected,setSelected] = useState<string>();
  const [detail,setDetail] = useState<AppVersion>();
  const [detailBusy,setDetailBusy] = useState(false), [detailError,setDetailError] = useState('');
  const [editing,setEditing] = useState(false), [draft,setDraft] = useState<VersionFields>(emptyDraft);
  const [reason,setReason] = useState(''), [publishing,setPublishing] = useState(false);
  const [confirmation,setConfirmation] = useState<Confirmation>();
  const [otp,setOtp] = useState(''), [busy,setBusy] = useState(false), [formError,setFormError] = useState('');
  const [recovery,setRecovery] = useState<Recovery>();
  const [feedback,setFeedback] = useState<{type:'success'|'warning';text:string}>();
  const lock = useRef(false), lifecycle = useRef(0);
  const canRead = session.permissions.includes('app_versions.read');
  const canDraft = session.permissions.includes('app_versions.draft');
  const canReadApproval = session.permissions.includes('approvals.read');
  useEffect(()=>{lifecycle.current++;return()=>{lifecycle.current++;};},[source,session.id]);
  useEffect(()=>{
    if(!canRead)return;
    let active=true;setListBusy(true);setListError('');setList(undefined);
    source.list(query).then(value=>{if(active)setList(value);}).catch(error=>{if(active)setListError(issue(error));}).finally(()=>{if(active)setListBusy(false);});
    return()=>{active=false;};
  },[source,query,refresh,canRead]);
  useEffect(()=>{
    if(!selected||!canRead)return;
    setDetail(undefined);setDetailError('');setFormError('');setPublishing(false);setReason('');
    if(selected==='new'){setDraft(recovery?.attempt.kind==='save'?normalizeDraft(recovery.attempt.payload):emptyDraft());setEditing(true);setDetailBusy(false);return;}
    let active=true;setDetailBusy(true);setEditing(false);
    source.get(selected).then(value=>{if(active){setDetail(value);setDraft(normalizeDraft(value));}}).catch(error=>{if(active)setDetailError(issue(error));}).finally(()=>{if(active)setDetailBusy(false);});
    return()=>{active=false;};
  },[source,selected,detailRefresh,canRead]);
  function open(id:string){if(lock.current||recovery)return;setFeedback(undefined);setSelected(id);setConfirmation(undefined);setFormError('');}
  function close(){if(lock.current||confirmation)return;setSelected(undefined);setEditing(false);setPublishing(false);setFormError('');}
  const inactive = busy || !!confirmation || !!recovery;
  const change = (key:keyof VersionFields,value:string|null) => setDraft(previous=>({...previous,[key]:value}));
  async function prepare(kind:'save'|'publish') {
    if(lock.current||recovery||confirmation||!selected)return;
    if(kind==='save'&&(!canDraft||(detail&&versionBlock(detail,session,'edit'))))return;
    if(kind==='publish'&&(!detail||versionBlock(detail,session,'publish')))return;
    const fields=normalizeDraft(draft),problem=kind==='save'?draftProblem(fields):!reason.trim()||[...reason.trim()].length>500?'请填写1至500字的发布理由':'';
    if(problem){setFormError(problem);return;}
    lock.current=true;setBusy(true);setFormError('');setFeedback(undefined);
    const current=lifecycle.current;
    try {
      if(kind==='save') {
        const previous=detail?structuredClone(detail):undefined;
        const payload=Object.freeze({...fields,operation_id:crypto.randomUUID(),...(previous?{expected_snapshot_hash:previous.snapshot_hash}:{})});
        const prepared=await source.prepareSave(payload,previous?{id:previous.id,snapshot_hash:previous.snapshot_hash}:undefined);
        if(current===lifecycle.current){setConfirmation({kind,payload,previous,prepared});setOtp('');}
      }else if(detail){
        const version=structuredClone(detail),text=reason.trim();
        const prepared=await source.preparePublish(version,text);
        if(current===lifecycle.current){setConfirmation({kind,version,reason:text,prepared});setOtp('');}
      }
    }catch(error){if(current===lifecycle.current)setFormError(issue(error));}
    finally{lock.current=false;if(current===lifecycle.current)setBusy(false);}
  }
  async function confirm() {
    const attempt=confirmation;
    if(!attempt||lock.current||recovery)return;
    if(!/^\d{6}$/.test(otp)){setFormError('请输入6位动态验证码');return;}
    lock.current=true;setBusy(true);setFormError('');const current=lifecycle.current;
    try {
      if(attempt.kind==='save'){
        const value=await attempt.prepared.confirm(otp);
        if(current!==lifecycle.current)return;
        setSelected(value.id);setDetail(value);setDraft(normalizeDraft(value));setEditing(false);
        setFeedback({type:'success',text:demo?'示例草稿已保存，未创建真实版本。':'草稿已保存。申请发布后仍须由另一位管理员复核。'});
      }else{
        const approval=await attempt.prepared.confirm(otp);
        if(current!==lifecycle.current)return;
        setDetail(previous=>previous?{...previous,pending_approval_id:approval.id,can_edit:false}:previous);
        setPublishing(false);setReason('');
        setFeedback({type:'success',text:demo?'示例发布申请已进入审批中心；未执行真实发布。':'发布申请已提交，等待另一位管理员审批。'});
      }
      setConfirmation(undefined);setRefresh(v=>v+1);
    }catch(error){
      if(current!==lifecycle.current)return;
      setConfirmation(undefined);
      if(error instanceof VersionWriteError&&!error.uncertain)setFormError(`${issue(error)}。业务资料尚未提交，请核对后重新确认。`);
      else setRecovery({attempt,error:issue(error)});
    }finally{lock.current=false;if(current===lifecycle.current){setBusy(false);setOtp('');}}
  }
  async function reconcile() {
    if(!recovery||lock.current)return;
    const attempt=recovery.attempt,current=lifecycle.current;
    lock.current=true;setBusy(true);setFormError('');
    try {
      const latest=attempt.kind==='save'?(attempt.previous?await source.get(attempt.previous.id):await source.find(attempt.payload.platform,attempt.payload.version_code)):await source.get(attempt.version.id);
      if(current!==lifecycle.current)return;
      if(!latest){setRecovery({attempt,error:'暂未找到这个平台和编号的版本。请稍后再查询，页面不会自动重复保存。'});return;}
      let text='已取得最新资料，请核对后再决定下一步。',matched=false;
      if(attempt.kind==='save'){
        matched=sameVersionFields(latest,attempt.payload);
        text=matched?'已核对到与刚才提交内容一致的版本资料，请以当前状态为准。':'最新版本资料与刚才提交的内容不同。请核对最新资料后重新编辑。';
      }else{
        const approvalId=latest.pending_approval_id??latest.approval_id;
        if(approvalId&&canReadApproval){
          const approval=await reviews.getApproval(approvalId);
          if(current!==lifecycle.current)return;
          matched=approval.target_id===latest.id&&approval.action_code==='app_versions.publish'&&approval.reason===attempt.reason&&approval.payload_hash===attempt.version.snapshot_hash;
        }
        text=matched?'已查询到对应发布申请，请在审批中心查看当前结果。':approvalId?'版本已有发布申请，请在审批中心核对，页面不会重新申请。':'已取得最新草稿，当前没有有效的待审批申请。请核对后再决定是否重新申请。';
      }
      setRecovery(undefined);setSelected(latest.id);setDetail(latest);setDraft(normalizeDraft(latest));setEditing(false);setPublishing(false);
      setFeedback({type:matched?'success':'warning',text});setRefresh(v=>v+1);
    }catch(error){if(current===lifecycle.current)setRecovery({attempt,error:issue(error)});}
    finally{lock.current=false;if(current===lifecycle.current)setBusy(false);}
  }
  const recoverySelection=recovery?.attempt.kind==='save'?(recovery.attempt.previous?.id??'new'):recovery?.attempt.version.id;
  if(!canRead)return <Alert type="warning" showIcon message="当前账号没有版本管理的查看权限"/>;
  const editBlock=detail?versionBlock(detail,session,'edit'):'';
  const publishBlock=detail?versionBlock(detail,session,'publish'):'';
  const pending=detail?.pending_approval_id??detail?.approval_id;
  return <>
    <div className="page-intro"><div><div className="eyebrow">APP RELEASES</div><h1>版本管理</h1><p>准备版本资料，让每次更新都有清楚的来路。</p></div><div className="version-top-actions"><Button icon={<ReloadOutlined/>} loading={listBusy} onClick={()=>setRefresh(v=>v+1)}>刷新</Button><Button type="primary" icon={<PlusOutlined/>} disabled={!canDraft||!!recovery||busy} onClick={()=>open('new')}>新建草稿</Button></div></div>
    <div className="version-guide"><FileProtectOutlined/><div><strong>草稿 → 发布申请 → 双人审批</strong><p>填写已有安装包的 HTTPS 域名链接和校验信息。这里管理版本资料，不上传或托管安装包。</p></div></div>
    {recovery&&!selected&&<Alert className="spaced" type="warning" showIcon message="上一笔操作的结果尚待核实" description="不会自动重新提交，请先查询最新资料。" action={<Button onClick={()=>setSelected(recoverySelection)}>核实结果</Button>}/>}
    <section className="surface spaced"><div className="table-toolbar"><strong>版本列表</strong><div className="version-filters"><Select aria-label="版本平台" value={query.platform??''} options={[{value:'',label:'全部平台'},{value:'ANDROID',label:'Android'},{value:'IOS',label:'iOS'}]} onChange={v=>setQuery({...query,platform:v==='ANDROID'||v==='IOS'?v:undefined,page:1})}/><Select aria-label="版本状态" value={query.status??''} options={[{value:'',label:'全部状态'},...Object.entries(versionStatusLabels).map(([value,label])=>({value,label}))]} onChange={v=>setQuery({...query,status:v==='DRAFT'||v==='PUBLISHED'||v==='WITHDRAWN'?v:undefined,page:1})}/></div></div>
      {listError?<div className="inset"><Alert type="error" showIcon message={listError} action={<Button onClick={()=>setRefresh(v=>v+1)}>重试</Button>}/></div>:<Table<AppVersion> rowKey="id" loading={listBusy} dataSource={list?.items??[]} scroll={{x:800}} locale={{emptyText:<Empty description="暂无符合条件的版本"/>}} pagination={{current:query.page,pageSize:query.page_size,total:boundedTotal(list?.total??'0'),showSizeChanger:true,pageSizeOptions:[10,20,50],showTotal:()=>`共 ${list?.total??'0'} 个版本`,onChange:(page,page_size)=>setQuery({...query,page,page_size})}} columns={[
        {title:'版本',key:'version',width:225,render:(_,v)=><div className="version-cell"><span className="version-platform-icon">{v.platform==='ANDROID'?<AndroidOutlined/>:<AppleOutlined/>}</span><div><strong>{platformLabels[v.platform]} {v.version_name}</strong><div className="cell-sub numeric">编号 {v.version_code} · 记录 #{v.id}</div></div></div>},
        {title:'最低支持编号',dataIndex:'min_supported_code',render:(v:string)=><span className="numeric">{v}</span>},
        {title:'状态',key:'status',render:(_,v)=><Tag color={statusColor(v)}>{statusText(v)}</Tag>},
        {title:'创建时间',dataIndex:'created_at',render:time,width:185},
        {title:'',key:'detail',fixed:'right',width:100,render:(_,v)=><Button type="link" disabled={!!recovery||busy} onClick={()=>open(v.id)}>查看详情</Button>},
      ]}/>}
    </section><p className="page-note">草稿保存不会影响已发布版本。待审批期间资料锁定；发布仍须另一位管理员确认。</p>
    <Drawer title={selected==='new'?'新建版本草稿':'版本详情'} open={!!selected} width={720} onClose={close} closable={!busy&&!confirmation} maskClosable={!busy&&!confirmation} destroyOnClose>
      {feedback&&<Alert className="version-feedback" type={feedback.type} showIcon message={feedback.text}/>}
      {recovery&&<Alert className="version-feedback" type="warning" showIcon message="本次操作结果尚未确认" description={<><p>{recovery.error}</p><p>核实只会读取最新资料，不会重复保存或申请。</p>{recovery.attempt.kind==='save'&&<details><summary>查询编号</summary><code className="breakable">{recovery.attempt.payload.operation_id}</code></details>}</>} action={<Button loading={busy} onClick={()=>void reconcile()}>重新查询结果</Button>}/>}
      {detailBusy?<Spin/>:detailError?<Alert type="error" message={detailError} action={<Button onClick={()=>setDetailRefresh(v=>v+1)}>重试</Button>}/>:<>
        {detail&&<div className="detail-heading"><div><span className="eyebrow">VERSION #{detail.id}</span><h2>{platformLabels[detail.platform]} {detail.version_name}</h2></div><Tag color={statusColor(detail)}>{statusText(detail)}</Tag></div>}
        {editing?<div className="version-form">
          <div className="version-form-grid"><label>平台<Select aria-label="草稿平台" value={draft.platform} disabled={inactive} options={[{value:'ANDROID',label:'Android'},{value:'IOS',label:'iOS'}]} onChange={v=>change('platform',v)}/></label><label>版本名称<Input aria-label="版本名称" value={draft.version_name} disabled={inactive} onChange={e=>change('version_name',e.target.value)} maxLength={64} placeholder="例如 1.2.0"/></label><label>版本编号<Input aria-label="版本编号" value={draft.version_code} disabled={inactive} onChange={e=>change('version_code',e.target.value)} inputMode="numeric" maxLength={10} placeholder="例如 120"/></label><label>最低支持编号<Input aria-label="最低支持编号" value={draft.min_supported_code} disabled={inactive} onChange={e=>change('min_supported_code',e.target.value)} inputMode="numeric" maxLength={10} placeholder="0 表示不设置最低限制"/></label></div>
          <label>安装包下载地址<Input aria-label="安装包下载地址" value={draft.download_url} disabled={inactive} onChange={e=>change('download_url',e.target.value)} maxLength={512} placeholder="https://你的下载域名/安装包"/><small>请使用稳定的公网 HTTPS 域名链接，不用IP直链，也不包含账号密码、查询参数或片段。此处不会上传文件。</small></label>
          <label>安装包 SHA256<Input.TextArea aria-label="安装包SHA256" value={draft.file_sha256} disabled={inactive} onChange={e=>change('file_sha256',e.target.value)} rows={2} maxLength={64} placeholder="安装包的64位校验值"/></label>
          <label>签名证书 SHA256 <small>{draft.platform==='ANDROID'?'Android 必填':'iOS 可选'}</small><Input.TextArea aria-label="签名证书SHA256" value={draft.cert_sha256??''} disabled={inactive} onChange={e=>change('cert_sha256',e.target.value)} rows={2} maxLength={64} placeholder="签名证书的64位指纹，不是私钥"/></label>
          <label>更新说明<Input.TextArea aria-label="更新说明" value={draft.release_notes} disabled={inactive} onChange={e=>change('release_notes',e.target.value)} rows={5} maxLength={16000} placeholder="说明此次更新内容、适用范围与注意事项"/></label>
          {formError&&<Alert type="error" showIcon message={formError}/>}
          <div className="version-actions"><Button disabled={inactive} onClick={()=>{if(detail){setEditing(false);setDraft(normalizeDraft(detail));setFormError('');}else close();}}>取消编辑</Button><Button type="primary" icon={<FileProtectOutlined/>} disabled={inactive||!canDraft} loading={busy} onClick={()=>void prepare('save')}>核对并保存草稿</Button></div>
        </div>:detail&&<>
          <VersionFacts value={detail}/>
          <Descriptions className="spaced" size="small" column={1} items={[{key:'created',label:'创建时间',children:time(detail.created_at)},{key:'published',label:'发布时间',children:time(detail.published_at)}]}/>
          {pending&&<Alert className="spaced" type="info" showIcon message={detail.pending_approval_id?'此版本正在等待独立审批，暂不能修改资料。':'此版本已完成发布审批。'} action={canReadApproval?<Link to={approvalLink(pending)}>查看审批单 #{pending}</Link>:undefined}/>}
          {editBlock&&<p className="page-note">{editBlock}</p>}
          {!editing&&formError&&<Alert className="spaced" type="error" showIcon message={formError}/>}
          {publishing&&<div className="version-publish-reason"><label>发布理由<Input.TextArea aria-label="发布理由" value={reason} onChange={e=>setReason(e.target.value)} disabled={inactive} rows={3} maxLength={1000} placeholder="为什么要发布这个版本？供另一位管理员复核"/></label><p>本次仅发起审批，申请人不能审批自己的申请。</p></div>}
          {publishBlock&&publishBlock!==editBlock&&<p className="page-note">{publishBlock}</p>}
          <div className="version-actions"><Button icon={<ReloadOutlined/>} disabled={inactive} onClick={()=>setDetailRefresh(v=>v+1)}>刷新详情</Button><Button icon={<EditOutlined/>} disabled={inactive||!!editBlock} onClick={()=>{setDraft(normalizeDraft(detail));setEditing(true);setPublishing(false);setFormError('');}}>编辑草稿</Button><Button type="primary" icon={<SendOutlined/>} disabled={inactive||!!publishBlock} loading={busy} onClick={()=>publishing?void prepare('publish'):setPublishing(true)}>{publishing?'核对发布申请':'申请发布'}</Button></div>
        </>}
      </>}
    </Drawer>
    <Modal title={confirmation?.kind==='save'?'核对草稿资料':'核对发布申请'} open={!!confirmation} width={660} onCancel={()=>{if(!lock.current){setConfirmation(undefined);setOtp('');setFormError('');}}} closable={!busy} maskClosable={!busy} destroyOnClose confirmLoading={busy} cancelButtonProps={{disabled:busy}} okText={confirmation?.kind==='save'?'确认保存草稿':'确认提交审批'} onOk={()=>void confirm()}>
      {confirmation&&<>
        <Alert type="info" showIcon message={confirmation.kind==='save'?'只保存草稿，不会直接发布。':'申请将进入审批中心，须由另一位管理员复核。'} description={demo?'本机演示：请使用页面顶部的演示验证码；不产生真实版本或发布。':'以下资料已固定。需要修改时，请取消本次确认返回编辑。'}/>
        <div className="version-confirm-facts"><VersionFacts value={confirmation.kind==='save'?confirmation.payload:confirmation.version}/></div>
        {confirmation.kind==='publish'&&<p className="version-confirm-reason"><strong>发布理由</strong><br/>{confirmation.reason}</p>}
        <label className="form-label">6位动态验证码<Input aria-label="版本操作动态验证码" value={otp} onChange={e=>setOtp(e.target.value.replace(/\D/g,'').slice(0,6))} inputMode="numeric" autoComplete="one-time-code" maxLength={6} disabled={busy} placeholder="输入验证器中的验证码"/></label>
        {formError&&<Alert type="error" showIcon message={formError}/>}
      </>}
    </Modal>
  </>;
}

function VersionFacts({value}:{value:VersionFields|AppVersion}) {
  return <Descriptions className="version-facts" bordered size="small" column={1} items={[
    {key:'platform',label:'平台 / 版本',children:`${platformLabels[value.platform]} ${value.version_name}`},
    {key:'code',label:'版本编号',children:<span className="numeric">{value.version_code}</span>},
    {key:'minimum',label:'最低支持编号',children:<span className="numeric">{value.min_supported_code}</span>},
    {key:'url',label:'下载地址',children:<span className="breakable">{value.download_url||'尚未填写'}</span>},
    {key:'file',label:'安装包 SHA256',children:<code className="breakable">{value.file_sha256||'尚未填写'}</code>},
    {key:'cert',label:'证书 SHA256',children:<code className="breakable">{value.cert_sha256||(value.platform==='ANDROID'?'尚未填写':'未设置')}</code>},
    {key:'notes',label:'更新说明',children:<span className="breakable">{value.release_notes||'暂无说明'}</span>},
  ]}/>;
}
