import {useEffect, useRef, useState} from 'react';
import {useSearchParams} from 'react-router-dom';
import {Alert, Button, Descriptions, Drawer, Empty, Input, Modal, Select, Space, Spin, Table, Tag, message} from 'antd';
import {CheckOutlined, CloseOutlined, CopyOutlined, ReloadOutlined, SafetyCertificateOutlined} from '@ant-design/icons';
import type {AdminSession} from '../api/client';
import type {Approval, ApprovalQuery, AuditEntry, Page, ReviewSource, Verification} from '../api/review';
import {actionLabel, boundedTotal, decisionBlock, statusLabels, validRange, targetLabel} from './review-model';
const timestamp = (date?: string | null) => date ? new Date(date).toLocaleString('zh-CN', {timeZone: 'Asia/Shanghai', hour12: false}) : '—';
const color = (status: string) => ({PENDING:'gold', EXECUTED:'green', REJECTED:'red', EXPIRED:'default', FAILED:'red', APPROVED:'blue', SUCCESS:'green', DENIED:'orange'}[status]);
const problem = (e: unknown) => e instanceof Error ? e.message : '请求失败，请重试';
const snapLabels: Record<string,string> = {id:'版本记录',platform:'平台',version_name:'版本名称',version_code:'版本编号',min_supported_code:'最低支持版本',download_url:'下载地址',file_sha256:'安装包 SHA256',cert_sha256:'证书 SHA256',release_notes:'更新说明',status:'原始状态',created_at:'创建时间',approval_id:'审批编号',published_at:'发布时间'};
const noPermission = <Alert type="warning" showIcon message="当前账号没有此页面的查看权限" />;

export function ApprovalsPage({source, session, demo = false}: {source: ReviewSource; session: AdminSession; demo?: boolean}) {
  const [searchParams]=useSearchParams();
  const [query, setQuery] = useState<ApprovalQuery>({page:1,page_size:20,scope:'pending'});
  const [data, setData] = useState<Page<Approval>>();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [refresh, setRefresh] = useState(0);
  const [selected, setSelected] = useState<string>();
  const [detail, setDetail] = useState<Approval>();
  const [detailError, setDetailError] = useState('');
  const [detailLoading, setDetailLoading] = useState(false);
  const [decision, setDecision] = useState<'approve' | 'reject'>();
  const [note, setNote] = useState('');
  const [code, setCode] = useState('');
  const [busy, setBusy] = useState(false);
  const [uncertain, setUncertain] = useState(false);
  const [decisionError, setDecisionError] = useState('');
  const [notice, contextHolder] = message.useMessage();
  const alive = useRef(true);
  const writeLock = useRef(false);
  const canRead = session.permissions.includes('approvals.read');
  useEffect(()=>{const id=searchParams.get('approval');if(id&&/^[1-9][0-9]{0,18}$/.test(id))setSelected(id);},[searchParams]);
  useEffect(() => {alive.current=true; return () => {alive.current=false;};}, []);
  useEffect(() => {
    if (!canRead) return;
    let current=true; setLoading(true); setError(''); setData(undefined);
    source.listApprovals(query).then(r => {if(current) setData(r);}).catch(e => {if(current) setError(problem(e));}).finally(() => {if(current) setLoading(false);});
    return () => {current=false;};
  }, [source, query, refresh, canRead]);
  useEffect(() => {
    if (!selected || !canRead) return;
    let current=true; setDetail(undefined); setDetailError(''); setDetailLoading(true);
    source.getApproval(selected).then(r => {if(current) {setDetail(r);setUncertain(false);}}).catch(e => {if(current) setDetailError(problem(e));}).finally(() => {if(current) setDetailLoading(false);});
    return () => {current=false;};
  }, [source, selected, refresh, canRead]);
  const startDecision = (d: 'approve' | 'reject') => {setDecision(d); setCode('');setNote('');setDecisionError('');};
  const submit = async () => {
    if (!detail || !decision || writeLock.current || uncertain) return;
    const blocked = decisionBlock(detail,session,decision);
    if (blocked) {setDecisionError(blocked); return;}
    if (!/^\d{6}$/.test(code)) {setDecisionError('请输入6位动态验证码'); return;}
    if (decision === 'reject' && (!note.trim() || note.trim().length>500)) {setDecisionError('请填写1至500字的拒绝理由');return;}
    writeLock.current=true; setBusy(true);setDecisionError('');
    try {
      const prepared = await source.prepareDecision(detail.id,decision,note.trim());
      const changed = await prepared.confirm(code);
      if (!alive.current) return;
      setDetail(changed);setDecision(undefined);setRefresh(x=>x+1);
      notice.success(demo ? '演示已更新；没有执行真实发布' : decision === 'approve' ? '审批已执行' : '申请已拒绝');
    } catch(e) {
      if (alive.current) {setDecisionError(problem(e));setUncertain(true);}
    } finally {writeLock.current=false;if(alive.current) {setBusy(false);setCode('');}}
  };
  if (!canRead) return noPermission;
  const blockApprove = detail ? decisionBlock(detail,session,'approve') : '';
  const blockReject = detail ? decisionBlock(detail,session,'reject') : '';
  return <>{contextHolder}
    <div className="page-intro"><div><div className="eyebrow">REVIEW CENTER</div><h1>审批中心</h1><p>每一次重要变更，都经过独立复核。</p></div><Button icon={<ReloadOutlined />} onClick={()=>setRefresh(x=>x+1)} loading={loading}>刷新列表</Button></div>
    <div className="review-rule"><SafetyCertificateOutlined /><span>双人复核</span><span>24小时有效</span><span>执行前核验资料</span><span>重复提交保护</span></div>
    <section className="surface">
      <div className="table-toolbar"><div className="view-tabs">{([['pending','待我审批'],['mine','我发起的'],['all','全部记录']] as const).map(([scope,label])=><button className={query.scope===scope?'active':''} key={scope} onClick={()=>setQuery({...query,scope,page:1})}>{label}</button>)}</div><Select aria-label="审批状态" value={query.status ?? ''} style={{width:145}} options={[{value:'',label:'全部状态'},...Object.entries(statusLabels).map(([value,label])=>({value,label}))]} onChange={status=>setQuery({...query,status,page:1})}/></div>
      {error ? <div className="inset"><Alert type="error" showIcon message={error} action={<Button onClick={()=>setRefresh(x=>x+1)}>重试</Button>}/></div> : <Table<Approval> rowKey="id" dataSource={data?.items ?? []} loading={loading} scroll={{x:850}} locale={{emptyText:<Empty description="暂时没有符合条件的审批"/>}} pagination={{current:query.page,pageSize:query.page_size,total:boundedTotal(data?.total ?? '0'),showSizeChanger:true,pageSizeOptions:[10,20,50],showTotal:()=>`共 ${data?.total ?? '0'} 条`,onChange:(page,page_size)=>setQuery({...query,page,page_size})}} columns={[
        {title:'审批事项',key:'action',width:170,render:(_,a)=><div><strong>{actionLabel(a.action_code)}</strong><div className="cell-sub">#{a.id} · {targetLabel(a.target_type)} #{a.target_id ?? '—'}</div></div>},
        {title:'申请理由',dataIndex:'reason',ellipsis:true,width:250},
        {title:'发起人',dataIndex:'requested_by',render:(id:string)=><span>{id===session.id?'我':`管理员 ${id}`}</span>},
        {title:'状态',dataIndex:'status',render:(s:Approval['status'])=><Tag color={color(s)}>{statusLabels[s]}</Tag>},
        {title:'申请时间',dataIndex:'created_at',render:timestamp,width:185},
        {title:'',key:'review',fixed:'right',width:92,render:(_,a)=><Button type="link" onClick={()=>setSelected(a.id)}>查看详情</Button>},
      ]}/>}
    </section>
    <div className="page-note">当前已接入版本发布审批。资金和奖励相关操作尚未接入执行。</div>
    <Drawer title="审批详情" open={!!selected} width={620} onClose={()=>{if(!busy){setSelected(undefined);setDecision(undefined);}}} maskClosable={!busy} closable={!busy} destroyOnClose>
      {detailLoading ? <Spin/> : detailError ? <Alert type="error" message={detailError} action={<Button onClick={()=>setRefresh(x=>x+1)}>重新查询</Button>}/> : detail && <>
        <div className="detail-heading"><div><span className="eyebrow">APPROVAL #{detail.id}</span><h2>{actionLabel(detail.action_code)}</h2></div><Tag color={color(detail.status)}>{statusLabels[detail.status]}</Tag></div>
        <Descriptions size="small" column={1} items={[{key:'author',label:'发起管理员',children:detail.requested_by===session.id?'我':detail.requested_by},{key:'reason',label:'申请理由',children:detail.reason},{key:'created',label:'发起时间',children:timestamp(detail.created_at)},{key:'expires',label:'到期时间',children:timestamp(detail.expires_at)},{key:'decider',label:'审批管理员',children:detail.decided_by ?? '尚未处理'},{key:'decided',label:'处理时间',children:timestamp(detail.decided_at)},{key:'note',label:'审批备注',children:detail.decision_note || '—'}]}/>
        <h3 className="section-label">提交时的资料</h3>
        {detail.snapshot ? <Descriptions bordered size="small" column={1} items={Object.entries(detail.snapshot).map(([key,value])=>({key,label:snapLabels[key] ?? key,children:<span className="breakable">{value ?? '—'}</span>}))}/> : <Alert message="此类操作尚未接入，当前不显示原始资料" type="info"/>}
        <details className="hash-details"><summary>查看资料校验码</summary><code>{detail.payload_hash}</code></details>
        {blockApprove && <Alert className="spaced" message={blockApprove} type="info" showIcon/>}
        {uncertain && <Alert className="spaced" type="warning" showIcon message="尚未确认本次操作结果" description="请重新查询最新详情，再决定下一步。页面不会自动重复提交。" action={<Button onClick={()=>{setDecision(undefined);setRefresh(x=>x+1);}}>重新查询</Button>}/>}
        <div className="decision-actions"><Button danger icon={<CloseOutlined/>} disabled={!!blockReject || uncertain} onClick={()=>startDecision('reject')}>拒绝申请</Button><Button type="primary" icon={<CheckOutlined/>} disabled={!!blockApprove || uncertain} onClick={()=>startDecision('approve')}>通过并执行</Button></div>
      </>}
    </Drawer>
    <Modal title={decision==='approve'?'复核并批准发布':'拒绝这份申请'} open={!!decision} onCancel={()=>{if(!busy)setDecision(undefined);}} maskClosable={!busy} closable={!busy} confirmLoading={busy} okText={demo?'确认演示操作':'确认提交'} okButtonProps={{disabled:uncertain,danger:decision==='reject'}} cancelButtonProps={{disabled:busy}} onOk={submit} destroyOnClose>
      {detail && <p>审批 #{detail.id} · {actionLabel(detail.action_code)}<br/><span className="muted">{detail.reason}</span></p>}
      {demo && <Alert type="info" showIcon message="这是演示操作，请使用页面顶部的演示验证码。"/>}
      {decision==='approve' && <p>确认上述资料无误。系统会再次核验资料，并在审批成功后发布这一版本。</p>}
      {decision==='reject' && <label className="form-label">拒绝理由<Input.TextArea aria-label="拒绝理由" value={note} disabled={busy || uncertain} onChange={e=>setNote(e.target.value)} maxLength={500} showCount rows={3} placeholder="说明需要补充或更正的资料"/></label>}
      <label className="form-label">6位动态验证码<Input aria-label="动态验证码" value={code} onChange={e=>setCode(e.target.value.replace(/\D/g,'').slice(0,6))} inputMode="numeric" autoComplete="one-time-code" maxLength={6} disabled={busy || uncertain} placeholder="输入验证器中的验证码"/></label>
      {decisionError && <Alert type="error" showIcon message={decisionError} description={uncertain?'操作结果尚未确认，请关闭此窗口并重新查询详情。':undefined}/>}
    </Modal>
  </>;
}

export function AuditPage({source,session,demo=false}: {source:ReviewSource;session:AdminSession;demo?:boolean}) {
  const [page,setPage]=useState(1); const [size,setSize]=useState(20); const [result,setResult]=useState('');
  const [data,setData]=useState<Page<AuditEntry>>(); const [loading,setLoading]=useState(false); const [error,setError]=useState('');const [refresh,setRefresh]=useState(0);
  const [from,setFrom]=useState('');const [to,setTo]=useState('');const [verifying,setVerifying]=useState(false);const [check,setCheck]=useState<Verification>();const [checkError,setCheckError]=useState('');
  const [notice,holder]=message.useMessage(); const generation=useRef(0);
  const canRead=session.permissions.includes('audit.read');
  useEffect(()=>{if(!canRead)return;let active=true;generation.current++;setCheck(undefined);setCheckError('');setVerifying(false);setLoading(true);setError('');setData(undefined);
    source.listAudit({page,page_size:size,result}).then(r=>{if(!active)return;setData(r);if(r.items.length){const ids=r.items.map(x=>x.id).sort((a,b)=>BigInt(a)<BigInt(b)?-1:1);setFrom(ids[0]);setTo(ids[ids.length-1]);}else{setFrom('');setTo('');}}).catch(e=>{if(active)setError(problem(e));}).finally(()=>{if(active)setLoading(false);});
    return()=>{active=false;};
  },[source,page,size,result,refresh,canRead]);
  useEffect(()=>{generation.current++;setCheck(undefined);setCheckError('');setVerifying(false);return()=>{generation.current++;};},[source,from,to]);
  const verify=async()=>{const current=++generation.current;setCheck(undefined);setCheckError('');if(!validRange(from,to)){setCheckError('请填写有效的日志起止编号，结束编号不能小于开始编号');return;}setVerifying(true);try{const r=await source.verify(from,to);if(current===generation.current)setCheck(r);}catch(e){if(current===generation.current)setCheckError(problem(e));}finally{if(current===generation.current)setVerifying(false);}};
  if(!canRead)return noPermission;
  return <>{holder}<div className="page-intro"><div><div className="eyebrow">OPERATION HISTORY</div><h1>操作日志</h1><p>查看处理过程，核验记录是否完整。</p></div><Button icon={<ReloadOutlined/>} onClick={()=>setRefresh(x=>x+1)} loading={loading}>刷新记录</Button></div>
    <div className="audit-check surface"><div><SafetyCertificateOutlined/><h3>日志完整性校验</h3><p>{demo?'演示校验仅核验示例记录。':'按固定快照核验指定区间，单次最多10000条。'}外部存证尚未接入。</p></div><div className="range-fields"><Input aria-label="开始日志编号" placeholder="开始编号" value={from} onChange={e=>setFrom(e.target.value)} inputMode="numeric"/><span>至</span><Input aria-label="结束日志编号" placeholder="结束编号" value={to} onChange={e=>setTo(e.target.value)} inputMode="numeric"/><Button type="primary" onClick={verify} loading={verifying} disabled={!from || !to || loading}>校验此区间</Button></div></div>
    {checkError&&<Alert className="spaced" type="error" showIcon message={checkError}/>}
    {check&&<Alert className="spaced" type={check.valid?'success':'error'} showIcon message={check.valid?`${demo?'示例记录：':''}所选区间校验通过，共 ${check.checked} 条`:`发现校验异常${check.first_broken_id?` · 日志 #${check.first_broken_id}`:''}`} description={<span>{check.reason || `范围 #${check.from_id} 至 #${check.to_id}`} · 此结果仅针对本次查询的快照，不等同于外部存证。</span>}/>}
    <section className="surface spaced"><div className="table-toolbar"><strong>处理记录</strong><Select aria-label="操作结果" style={{width:145}} value={result} onChange={v=>{setResult(v);setPage(1);}} options={[{value:'',label:'全部结果'},{value:'SUCCESS',label:'操作成功'},{value:'DENIED',label:'已阻止'},{value:'FAILED',label:'操作失败'}]}/></div>
    {error?<div className="inset"><Alert type="error" message={error} action={<Button onClick={()=>setRefresh(x=>x+1)}>重试</Button>}/></div>:<Table<AuditEntry> rowKey="id" dataSource={data?.items ?? []} loading={loading} scroll={{x:850}} locale={{emptyText:<Empty description="暂无操作日志"/>}} pagination={{current:page,pageSize:size,total:boundedTotal(data?.total ?? '0'),showTotal:()=>`共 ${data?.total ?? '0'} 条`,showSizeChanger:true,pageSizeOptions:[10,20,50],onChange:(p,s)=>{setPage(p);setSize(s);}}} columns={[
      {title:'日志编号',dataIndex:'id',render:(id:string)=><span className="numeric">#{id}</span>,width:100},
      {title:'操作',dataIndex:'action_code',render:actionLabel},
      {title:'目标',key:'target',render:(_,r)=><div>{targetLabel(r.target_type)}<div className="cell-sub">#{r.target_id ?? '—'}{r.approval_id?` · 审批 #${r.approval_id}`:''}</div></div>},
      {title:'操作人',dataIndex:'admin_user_id',render:(id:string|null)=>id?`管理员 ${id}`:'系统'},
      {title:'结果',dataIndex:'result',render:(r:string)=><Tag color={color(r)}>{{SUCCESS:'成功',FAILED:'失败',DENIED:'已阻止'}[r]}</Tag>},
      {title:'发生时间',dataIndex:'created_at',render:timestamp,width:185},
      {title:'校验信息',key:'hash',width:100,render:(_,r)=><Button type="text" aria-label={`复制日志 ${r.id} 校验码`} icon={<CopyOutlined/>} onClick={async()=>{try{await navigator.clipboard.writeText(r.row_hash);notice.success('校验码已复制');}catch{notice.error('复制失败，请检查浏览器权限');}}}>复制</Button>},
    ]}/>}</section>
  </>;
}
