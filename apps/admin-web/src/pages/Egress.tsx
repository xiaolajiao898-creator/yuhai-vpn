import {useCallback,useEffect,useMemo,useState} from 'react';
import {Alert,Button,Checkbox,Drawer,Empty,Input,InputNumber,Modal,Segmented,Select,Space,Table,Tag,Tooltip} from 'antd';
import {CloudUploadOutlined,LinkOutlined,ReloadOutlined,SearchOutlined} from '@ant-design/icons';
import type {AdminSession} from '../api/client';
import {egressProtocols,optionsProblem,protocolLabels,queryProblem,type EgressItem,type EgressPage,type EgressQuery,type EgressSource,type EgressStatus,type EgressUpdate,type ImportOptions,type ImportResult,type PreviewResult,type Subscription,type SubscriptionInput} from '../api/egress';
import {userTime} from './users-model';
import './egress.css';

// 出口线路：外购代理与自建多协议节点。用户设备直接连接所选出口，后台只负责登记与下发，不中转流量。
// 状态分开呈现：「登记状态」来自本后台；「连通性」目前没有自动检测——不以绿色圆点冒充在线。
const countryNames:Record<string,string>={SG:'新加坡',US:'美国',JP:'日本',HK:'中国香港',KR:'韩国',MY:'马来西亚',TW:'中国台湾',GB:'英国',DE:'德国'};
const flag=(c:string)=>/^[A-Z]{2}$/.test(c)?String.fromCodePoint(...[...c].map(ch=>0x1F1E6+ch.charCodeAt(0)-65)):'🌐';
const newOp=()=>crypto.randomUUID();
const defaultOptions:ImportOptions={default_protocol:'SOCKS5',default_udp:false,country_code:'',city:'',region:'',name_prefix:'',min_tier:1,enable:false,update_existing:false};
const sample=`# 每行一条，支持：IP:端口:账号:密码、账号:密码@IP:端口、各协议分享链接、CSV、JSON、base64 订阅内容
203.0.113.10:1080:user1:pass1
socks5://user2:pass2@203.0.113.11:1080?udp=1#新加坡住宅
trojan://password@sg1.example.com:443?sni=sg1.example.com#新加坡 Trojan`;

function errorText(e:unknown){const s=(e as {status?:number})?.status;return s===401?'登录已失效，请重新登录':s===403?'当前账号没有出口管理权限':e instanceof Error?e.message:'操作失败，请重试';}

export function EgressPage({source,session}:{source:EgressSource;session:AdminSession}){
  const canRead=session.permissions.includes('egress.read');
  const canWrite=session.permissions.includes('egress.write');
  const [tab,setTab]=useState<'list'|'subs'>('list');
  const [query,setQuery]=useState<EgressQuery>({page:1,page_size:20});
  const [search,setSearch]=useState(''),[country,setCountry]=useState(''),[problem,setProblem]=useState('');
  const [page,setPage]=useState<EgressPage>(),[loading,setLoading]=useState(false),[error,setError]=useState('');
  const [refresh,setRefresh]=useState(0);
  const [importOpen,setImportOpen]=useState(false),[editing,setEditing]=useState<EgressItem>();
  const [busy,setBusy]=useState<string>();
  useEffect(()=>{
    if(!canRead||tab!=='list')return;
    const ctrl=new AbortController();setLoading(true);setError('');
    source.list(query,ctrl.signal).then(p=>{if(!ctrl.signal.aborted)setPage(p);}).catch(e=>{if(!ctrl.signal.aborted){setPage(undefined);setError(errorText(e));}}).finally(()=>{if(!ctrl.signal.aborted)setLoading(false);});
    return ()=>ctrl.abort();
  },[source,query,refresh,canRead,tab]);
  const items=page?.items??[];
  async function toggle(it:EgressItem){
    const next:EgressStatus=it.status==='ENABLED'?'DISABLED':'ENABLED';
    setBusy(it.id);
    try{await source.update(newOp(),it.id,{status:next});setRefresh(x=>x+1);}catch(e){Modal.error({title:'没有修改成功',content:errorText(e)});}finally{setBusy(undefined);}
  }
  function applyFilters(){const next={...query,page:1,search:search.trim()||undefined,country_code:country.trim().toUpperCase()||undefined};const p=queryProblem(next);setProblem(p);if(!p)setQuery(next);}
  if(!canRead)return <Alert type="warning" showIcon message="当前账号没有出口查看权限（egress.read）"/>;
  return <div className="egress-page">
    <div className="page-intro"><div><div className="eyebrow">EGRESS</div><h1>出口线路</h1><p>外购代理与自建多协议节点。用户设备直接连接所选出口，本后台只登记与按权益下发配置，不中转上网流量。</p></div>
      <Space wrap>{canWrite&&<Button type="primary" icon={<CloudUploadOutlined/>} onClick={()=>setImportOpen(true)}>批量导入</Button>}<Button icon={<ReloadOutlined/>} loading={loading} onClick={()=>setRefresh(x=>x+1)}>刷新</Button></Space></div>
    <div className="egress-facts">
      <section><span>符合筛选的出口</span><strong className="numeric">{page?.total??'—'}</strong><small>全部分页合计</small></section>
      <section><span>本页已启用</span><strong className="numeric">{page?items.filter(i=>i.status==='ENABLED').length:'—'}</strong><small>启用 = 会下发给有权益的设备</small></section>
      <section><span>连通性检测</span><strong className="muted-strong">尚未接入</strong><small>第三方代理没有心跳；连接结果以客户端实际连接为准</small></section>
    </div>
    <Segmented className="spaced" value={tab} onChange={v=>setTab(v as 'list'|'subs')} options={[{value:'list',label:'出口列表'},{value:'subs',label:'订阅来源'}]}/>
    {tab==='subs'?<Subscriptions source={source} canWrite={canWrite}/>:<section className="surface spaced">
      <form className="egress-filters" onSubmit={e=>{e.preventDefault();applyFilters();}}>
        <label className="grow">名称或主机<Input aria-label="搜索出口" prefix={<SearchOutlined/>} value={search} onChange={e=>setSearch(e.target.value)} placeholder="例如 新加坡、203.0.113.10" allowClear maxLength={64}/></label>
        <label>国家代码<Input aria-label="国家代码" value={country} onChange={e=>setCountry(e.target.value)} placeholder="SG / US / JP" maxLength={2} allowClear/></label>
        <label>协议<Select aria-label="协议筛选" value={query.protocol??''} options={[{value:'',label:'全部协议'},...egressProtocols.map(p=>({value:p,label:protocolLabels[p]}))]} onChange={v=>setQuery({...query,page:1,protocol:v||undefined})}/></label>
        <label>登记状态<Select aria-label="状态筛选" value={query.status??''} options={[{value:'',label:'全部'},{value:'ENABLED',label:'已启用'},{value:'DISABLED',label:'已停用'}]} onChange={v=>setQuery({...query,page:1,status:(v||undefined) as EgressStatus|undefined})}/></label>
        <Button type="primary" htmlType="submit">查询</Button><Button onClick={()=>{setSearch('');setCountry('');setProblem('');setQuery({page:1,page_size:query.page_size});}}>重置</Button>
      </form>
      {problem&&<div className="inset"><Alert showIcon type="error" message={problem}/></div>}
      {error&&<div className="inset"><Alert showIcon type="error" message={error} action={<Button onClick={()=>setRefresh(x=>x+1)}>重新查询</Button>}/></div>}
      <div className="table-toolbar"><strong>出口列表</strong><span>{page?`读取于 ${userTime(page.observed_at)}`:'正在读取'}</span></div>
      <Table<EgressItem> rowKey="id" loading={loading} dataSource={items} scroll={{x:1180}} locale={{emptyText:<Empty description={query.search||query.protocol||query.status||query.country_code?'没有符合筛选的出口':'还没有出口，点「批量导入」添加'}/>}}
        pagination={{current:query.page,pageSize:query.page_size,total:Number(page?.total??0),showSizeChanger:false,onChange:p=>setQuery({...query,page:p})}}
        columns={[
          {title:'出口',key:'name',width:240,render:(_,it)=><div className="egress-name"><span className="egress-flag">{flag(it.country_code)}</span><div><strong>{it.name}</strong><div className="cell-sub">{[countryNames[it.country_code]??(it.country_code||'未标注地区'),it.city].filter(Boolean).join(' · ')}</div></div></div>},
          {title:'协议',key:'protocol',render:(_,it)=><Space size={4} wrap><Tag>{protocolLabels[it.protocol]??it.protocol}</Tag>{it.params.security==='reality'&&<Tag>Reality</Tag>}{it.supports_udp?<Tag color="blue">UDP</Tag>:<Tooltip title="该出口不支持 UDP：客户端会如实提示"><Tag>无 UDP</Tag></Tooltip>}</Space>},
          {title:'地址',key:'addr',render:(_,it)=><span className="mono">{it.host.includes(':')?`[${it.host}]`:it.host}:{it.port}</span>},
          {title:'身份',key:'id',render:(_,it)=><span className="mono muted">{it.identity_hint||'—'}</span>},
          {title:'最低等级',dataIndex:'min_tier',render:v=>`L${v}`},
          {title:'登记状态',key:'status',render:(_,it)=>it.status==='ENABLED'?<Tag color="green">已启用</Tag>:<Tag>已停用</Tag>},
          {title:'来源',key:'source',render:(_,it)=>it.source==='SUBSCRIPTION'?<Tag icon={<LinkOutlined/>}>订阅</Tag>:<Tag>导入</Tag>},
          {title:'更新',dataIndex:'updated_at',render:v=><span className="muted">{userTime(v)}</span>},
          {title:'',key:'ops',fixed:'right',render:(_,it)=>canWrite?<Space><Button size="small" loading={busy===it.id} onClick={()=>toggle(it)}>{it.status==='ENABLED'?'停用':'启用'}</Button><Button size="small" type="link" onClick={()=>setEditing(it)}>编辑</Button></Space>:null},
        ]}/>
    </section>}
    <p className="page-note">凭据（密码、UUID 等）加密保存，任何列表、日志和审计里都只显示脱敏提示。停用只影响之后的下发；外购共享账号在用户设备上已拿到的材料无法由本后台立即收回，需依赖供应商子账号或更换凭据。</p>
    {importOpen&&<ImportDialog source={source} onClose={changed=>{setImportOpen(false);if(changed)setRefresh(x=>x+1);}}/>}
    {editing&&<EditDrawer source={source} item={editing} onClose={changed=>{setEditing(undefined);if(changed)setRefresh(x=>x+1);}}/>}
  </div>;
}

function ImportDialog({source,onClose}:{source:EgressSource;onClose:(changed:boolean)=>void}){
  const [text,setText]=useState(''),[opts,setOpts]=useState<ImportOptions>(defaultOptions);
  const [preview,setPreview]=useState<PreviewResult>(),[previewedFor,setPreviewedFor]=useState('');
  const [result,setResult]=useState<ImportResult>(),[error,setError]=useState(''),[loading,setLoading]=useState(false);
  // 同一份「文本 + 选项」只用一个操作编号：网络中断后重试不会重复导入。
  const fingerprint=useMemo(()=>JSON.stringify([text,opts]),[text,opts]);
  const [op,setOp]=useState(newOp);
  useEffect(()=>{setOp(newOp());},[fingerprint]);
  const stale=preview!==undefined&&previewedFor!==fingerprint;
  const set=<K extends keyof ImportOptions>(k:K,v:ImportOptions[K])=>setOpts(o=>({...o,[k]:v}));
  async function runPreview(){
    const p=optionsProblem(opts);if(p){setError(p);return;}
    if(!text.trim()){setError('请先粘贴要导入的内容');return;}
    setLoading(true);setError('');
    try{setPreview(await source.preview(text,opts));setPreviewedFor(fingerprint);}catch(e){setError(errorText(e));}finally{setLoading(false);}
  }
  async function runImport(){
    setLoading(true);setError('');
    try{setResult(await source.importNow(op,text,opts));}catch(e){setError(`${errorText(e)}。可以直接再点一次「确认导入」重试，不会重复导入。`);}finally{setLoading(false);}
  }
  const s=preview?.summary;
  const canImport=!!s&&!stale&&s.valid>0&&!result;
  return <Modal open width={980} title="批量导入出口" onCancel={()=>onClose(!!result)} maskClosable={false}
    footer={result?<Button type="primary" onClick={()=>onClose(true)}>完成</Button>:<Space><Button onClick={()=>onClose(false)}>取消</Button><Button onClick={runPreview} loading={loading&&!preview}>预览（不写入）</Button><Button type="primary" disabled={!canImport} loading={loading&&!!preview} onClick={runImport}>{s?`确认导入 ${s.valid} 条`:'确认导入'}</Button></Space>}>
    {result?<div className="import-result">
      <Alert type={result.failed?'warning':'success'} showIcon message={`新增 ${result.created} 条，更新 ${result.updated} 条，跳过 ${result.skipped} 条，失败 ${result.failed} 条`} description={opts.enable?'新增的出口已启用，会下发给有权益的设备。':'新增的出口默认停用，核对后在列表里逐条启用。'}/>
      {result.rows.length>0&&<Table size="small" className="spaced" rowKey="line" pagination={false} dataSource={result.rows} columns={[{title:'行',dataIndex:'line',width:60},{title:'内容（脱敏）',dataIndex:'source'},{title:'原因',dataIndex:'error'}]}/>}
    </div>:<>
      <div className="import-grid">
        <label className="import-text">导入内容<Input.TextArea aria-label="导入内容" value={text} onChange={e=>setText(e.target.value)} placeholder={sample} autoSize={{minRows:9,maxRows:16}} spellCheck={false}/></label>
        <div className="import-options">
          <label>无协议前缀的行按<Select value={opts.default_protocol} onChange={v=>{set('default_protocol',v);if(v!=='SOCKS5')set('default_udp',false);}} options={[{value:'SOCKS5',label:'SOCKS5'},{value:'HTTP',label:'HTTP 代理'},{value:'HTTPS',label:'HTTPS 代理'}]}/></label>
          <Checkbox checked={opts.default_udp} disabled={opts.default_protocol!=='SOCKS5'} onChange={e=>set('default_udp',e.target.checked)}>供应商确认支持 UDP（仅 SOCKS5）</Checkbox>
          <div className="two"><label>国家代码<Input value={opts.country_code} maxLength={2} placeholder="SG" onChange={e=>set('country_code',e.target.value.toUpperCase())}/></label><label>城市<Input value={opts.city} maxLength={64} placeholder="新加坡" onChange={e=>set('city',e.target.value)}/></label></div>
          <div className="two"><label>地区分组<Select value={opts.region} onChange={v=>set('region',v)} options={[{value:'',label:'不设置'},{value:'ASIA',label:'亚洲'},{value:'AMERICAS',label:'美洲'},{value:'EUROPE',label:'欧洲'},{value:'OCEANIA',label:'大洋洲'}]}/></label><label>最低套餐等级<InputNumber min={1} max={9} value={opts.min_tier} onChange={v=>set('min_tier',Number(v??1))}/></label></div>
          <label>名称前缀<Input value={opts.name_prefix} maxLength={48} placeholder="例如 新加坡住宅" onChange={e=>set('name_prefix',e.target.value)}/></label>
          <Checkbox checked={opts.update_existing} onChange={e=>set('update_existing',e.target.checked)}>已存在的出口用新内容覆盖</Checkbox>
          <Checkbox checked={opts.enable} onChange={e=>set('enable',e.target.checked)}>导入后立即启用</Checkbox>
        </div>
      </div>
      {error&&<Alert className="spaced" type="error" showIcon message={error}/>}
      {stale&&<Alert className="spaced" type="info" showIcon message="内容或选项已修改，请重新预览后再导入"/>}
      {preview&&!stale&&<div className="spaced">
        <div className="preview-summary"><span>共 <b>{s!.total}</b> 行</span><span className="ok">可导入 <b>{s!.valid}</b></span><span className="bad">有问题 <b>{s!.invalid}</b></span><span>重复 <b>{s!.duplicate}</b></span><span>已存在 <b>{s!.existing}</b></span></div>
        <Table size="small" rowKey="line" dataSource={preview.rows} pagination={{pageSize:50,hideOnSinglePage:true}} scroll={{y:320,x:760}}
          rowClassName={r=>r.error?'row-bad':''}
          columns={[{title:'行',dataIndex:'line',width:56},{title:'内容（脱敏）',dataIndex:'source',render:v=><span className="mono">{v}</span>},
            {title:'协议',dataIndex:'protocol',width:110,render:v=>v?protocolLabels[v]??v:'—'},
            {title:'UDP',dataIndex:'supports_udp',width:64,render:(v,r)=>r.error?'—':v?'支持':'不支持'},
            {title:'结果',key:'r',render:(_,r)=>r.error?<span className="bad-text">{r.error}</span>:r.existing_id?<span>已存在（{r.existing_id}）{opts.update_existing?'，将覆盖':'，将跳过'}</span>:<span className="ok-text">可导入</span>}]}/>
      </div>}
    </>}
  </Modal>;
}

function EditDrawer({source,item,onClose}:{source:EgressSource;item:EgressItem;onClose:(changed:boolean)=>void}){
  const [form,setForm]=useState({name:item.name,country_code:item.country_code,city:item.city,region:item.region,min_tier:item.min_tier,supports_udp:item.supports_udp,capacity_users:item.capacity_users});
  const [error,setError]=useState(''),[saving,setSaving]=useState(false);
  const change=useMemo(()=>{const c:EgressUpdate={};
    if(form.name!==item.name)c.name=form.name.trim();if(form.country_code!==item.country_code)c.country_code=form.country_code;
    if(form.city!==item.city)c.city=form.city;if(form.region!==item.region)c.region=form.region;if(form.min_tier!==item.min_tier)c.min_tier=form.min_tier;
    if(form.supports_udp!==item.supports_udp)c.supports_udp=form.supports_udp;
    if(form.capacity_users!==item.capacity_users)c.capacity_users=form.capacity_users;return c;},[form,item]);
  const [op]=useState(newOp);
  const save=useCallback(async()=>{
    if(!Object.keys(change).length){onClose(false);return;}
    if(change.country_code!==undefined&&change.country_code!==''&&!/^[A-Z]{2}$/.test(change.country_code)){setError('国家代码应为两位大写字母');return;}
    setSaving(true);setError('');
    try{await source.update(op,item.id,change);onClose(true);}catch(e){setError(errorText(e));}finally{setSaving(false);}
  },[change,item.id,onClose,op,source]);
  return <Drawer open width={420} title={`编辑出口 · ${item.id}`} onClose={()=>onClose(false)} extra={<Button type="primary" loading={saving} onClick={save}>保存</Button>}>
    <div className="edit-form">
      <label>名称<Input value={form.name} maxLength={64} onChange={e=>setForm({...form,name:e.target.value})}/></label>
      <div className="two"><label>国家代码<Input value={form.country_code} maxLength={2} onChange={e=>setForm({...form,country_code:e.target.value.toUpperCase()})}/></label><label>城市<Input value={form.city} maxLength={64} onChange={e=>setForm({...form,city:e.target.value})}/></label></div>
      <label>地区分组<Select value={form.region} onChange={v=>setForm({...form,region:v})} options={[{value:'',label:'不设置'},{value:'ASIA',label:'亚洲'},{value:'AMERICAS',label:'美洲'},{value:'EUROPE',label:'欧洲'},{value:'OCEANIA',label:'大洋洲'}]}/></label>
      <label>最低套餐等级<InputNumber min={1} max={9} value={form.min_tier} onChange={v=>setForm({...form,min_tier:Number(v??1)})}/></label>
      <label>人数上限<InputNumber min={0} max={1000000} value={form.capacity_users} onChange={v=>setForm({...form,capacity_users:Number(v??0)})}/><span className="muted">0 表示不设上限。出口没有心跳，这里不是在线人数。</span></label>
      <Checkbox checked={form.supports_udp} disabled={item.protocol!=='SOCKS5'} onChange={e=>setForm({...form,supports_udp:e.target.checked})}>支持 UDP{item.protocol!=='SOCKS5'&&'（由协议决定，不能修改）'}</Checkbox>
      <p className="muted">地址、端口与凭据不能在这里修改：请用「批量导入」并勾选「已存在的出口用新内容覆盖」。</p>
      {error&&<Alert type="error" showIcon message={error}/>}
    </div>
  </Drawer>;
}

function Subscriptions({source,canWrite}:{source:EgressSource;canWrite:boolean}){
  const [list,setList]=useState<Subscription[]>(),[error,setError]=useState(''),[refresh,setRefresh]=useState(0);
  const [adding,setAdding]=useState(false),[busy,setBusy]=useState<string>(),[message,setMessage]=useState('');
  useEffect(()=>{const c=new AbortController();source.subscriptions(c.signal).then(v=>{if(!c.signal.aborted)setList(v);}).catch(e=>{if(!c.signal.aborted)setError(errorText(e));});return ()=>c.abort();},[source,refresh]);
  async function fetchNow(s:Subscription){
    setBusy(s.id);setMessage('');
    try{const r=await source.refreshSubscription(newOp(),s.id,false);setMessage(`「${s.name}」：新增 ${r.created}，更新 ${r.updated}，失败 ${r.failed}，停用（订阅中已消失）${r.disabled}`);setRefresh(x=>x+1);}
    catch(e){setMessage(`「${s.name}」抓取失败：${errorText(e)}（出口没有被改动）`);}finally{setBusy(undefined);}
  }
  return <section className="surface spaced">
    <div className="table-toolbar"><strong>订阅来源</strong>{canWrite&&<Button onClick={()=>setAdding(true)}>添加订阅</Button>}</div>
    <div className="inset"><Alert type="info" showIcon message="订阅只抓取 https 公网地址（443 端口），拒绝内网/本机地址与重定向，不携带任何后台凭据；完整订阅地址加密保存，这里只显示域名。抓取失败不会改动已有出口。"/></div>
    {error&&<div className="inset"><Alert type="error" showIcon message={error}/></div>}
    {message&&<div className="inset"><Alert type="success" showIcon message={message} closable onClose={()=>setMessage('')}/></div>}
    <Table<Subscription> rowKey="id" loading={!list&&!error} dataSource={list??[]} pagination={false} scroll={{x:760}} locale={{emptyText:<Empty description="还没有订阅来源"/>}}
      columns={[{title:'名称',dataIndex:'name',render:(v,s)=><div><strong>{v}</strong><div className="cell-sub mono">{s.url_hint}</div></div>},
        {title:'默认协议',dataIndex:'default_protocol',render:v=>protocolLabels[v]??v},
        {title:'地区',key:'c',render:(_,s)=>`${flag(s.country_code)} ${countryNames[s.country_code]??(s.country_code||'—')}`},
        {title:'最近抓取',key:'f',render:(_,s)=><div><div>{s.last_fetched_at?userTime(s.last_fetched_at):'尚未抓取'}</div><div className="cell-sub">{s.last_result}</div></div>},
        {title:'',key:'op',render:(_,s)=>canWrite?<Button size="small" loading={busy===s.id} onClick={()=>fetchNow(s)}>立即抓取</Button>:null}]}/>
    {adding&&<AddSubscription source={source} onClose={changed=>{setAdding(false);if(changed)setRefresh(x=>x+1);}}/>}
  </section>;
}

function AddSubscription({source,onClose}:{source:EgressSource;onClose:(changed:boolean)=>void}){
  const [input,setInput]=useState<SubscriptionInput>({name:'',url:'',default_protocol:'SOCKS5',country_code:'',region:'',min_tier:1});
  const [preview,setPreview]=useState<PreviewResult>(),[error,setError]=useState(''),[loading,setLoading]=useState(false);
  const [op]=useState(newOp);
  const problem=!input.name.trim()?'请填写名称':!/^https:\/\//i.test(input.url)?'订阅地址必须以 https:// 开头':input.country_code&&!/^[A-Z]{2}$/.test(input.country_code)?'国家代码应为两位大写字母':'';
  async function run(fn:()=>Promise<void>){if(problem){setError(problem);return;}setLoading(true);setError('');try{await fn();}catch(e){setError(errorText(e));}finally{setLoading(false);}}
  return <Modal open width={760} title="添加订阅来源" onCancel={()=>onClose(false)} maskClosable={false}
    footer={<Space><Button onClick={()=>onClose(false)}>取消</Button><Button loading={loading&&!preview} onClick={()=>run(async()=>setPreview(await source.previewSubscription(input)))}>抓取预览（不写入）</Button><Button type="primary" disabled={!preview} loading={loading&&!!preview} onClick={()=>run(async()=>{await source.createSubscription(op,input);onClose(true);})}>保存订阅</Button></Space>}>
    <div className="edit-form">
      <label>名称<Input value={input.name} maxLength={64} onChange={e=>{setInput({...input,name:e.target.value});}}/></label>
      <label>订阅地址<Input.Password value={input.url} visibilityToggle placeholder="https://..." onChange={e=>{setInput({...input,url:e.target.value});setPreview(undefined);}}/></label>
      <div className="two"><label>默认协议<Select value={input.default_protocol} onChange={v=>setInput({...input,default_protocol:v})} options={[{value:'SOCKS5',label:'SOCKS5'},{value:'HTTP',label:'HTTP 代理'},{value:'HTTPS',label:'HTTPS 代理'}]}/></label><label>国家代码<Input value={input.country_code} maxLength={2} onChange={e=>setInput({...input,country_code:e.target.value.toUpperCase()})}/></label></div>
      <div className="two"><label>地区分组<Select value={input.region} onChange={v=>setInput({...input,region:v})} options={[{value:'',label:'不设置'},{value:'ASIA',label:'亚洲'},{value:'AMERICAS',label:'美洲'},{value:'EUROPE',label:'欧洲'}]}/></label><label>最低套餐等级<InputNumber min={1} max={9} value={input.min_tier} onChange={v=>setInput({...input,min_tier:Number(v??1)})}/></label></div>
      {error&&<Alert type="error" showIcon message={error}/>}
      {preview&&<Alert type={preview.summary.invalid?'warning':'success'} showIcon message={`预览：共 ${preview.summary.total} 行，可导入 ${preview.summary.valid}，有问题 ${preview.summary.invalid}，重复 ${preview.summary.duplicate}`} description="保存后可在列表中「立即抓取」；抓取到的出口默认停用。"/>}
    </div>
  </Modal>;
}
