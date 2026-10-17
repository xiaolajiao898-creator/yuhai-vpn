import type {AdminSession} from '../api/client.ts';
import type {EgressItem,EgressSource,ImportOptions,ImportResult,PreviewResult,PreviewRow,Subscription} from '../api/egress.ts';

// 演示用内存数据源：只模拟界面流程（预览 → 逐行报错 → 确认导入 → 启停），解析规则是简化版，
// 真实规则以服务端 internal/egress 为准。不发任何网络请求，不含任何真实凭据。
const schemes:Record<string,string>={socks5:'SOCKS5',socks:'SOCKS5',http:'HTTP',https:'HTTPS',ss:'SHADOWSOCKS',trojan:'TROJAN',vless:'VLESS',vmess:'VMESS',hysteria2:'HYSTERIA2',hy2:'HYSTERIA2'};
const now=()=>new Date().toISOString();
const privateHost=(h:string)=>/^(127\.|10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.|localhost$)/.test(h);

function parse(text:string,o:ImportOptions,existing:Map<string,string>):{rows:PreviewRow[];valid:{row:PreviewRow;key:string}[]}{
  const rows:PreviewRow[]=[];const valid:{row:PreviewRow;key:string}[]=[];const seen=new Map<string,number>();
  text.split(/\r?\n/).forEach((raw,i)=>{
    const line=raw.trim();if(!line||line.startsWith('#'))return;
    const n=i+1;const mask=(p:string)=>`${p}…（${line.length} 字符）`;
    let protocol=o.default_protocol as string,host='',port=0,user='',udp=o.default_udp;
    const m=/^([a-z0-9]+):\/\/(?:([^@]*)@)?\[?([^\]/?#:]+)\]?:(\d{1,5})/i.exec(line);
    if(m){
      const p=schemes[m[1].toLowerCase()];
      if(m[1].toLowerCase()==='tuic'){rows.push({line:n,source:mask('tuic://'),supports_udp:false,error:'当前内核（Xray）不支持 TUIC'});return;}
      if(!p){rows.push({line:n,source:mask(`${m[1]}://`),supports_udp:false,error:'不支持的协议'});return;}
      if(/allowInsecure|insecure=1|skip-cert-verify/i.test(line)){rows.push({line:n,source:mask(`${m[1]}://`),supports_udp:false,error:'不允许关闭 TLS 证书校验（自签证书请改用 pin_sha256）'});return;}
      protocol=p;user=m[2]??'';host=m[3];port=+m[4];udp=p==='SOCKS5'?/udp=1/.test(line):!['HTTP','HTTPS'].includes(p);
    }else{
      const c=/^([^:\s]+):(\d{1,5})(?::([^:]*):(.*))?$/.exec(line);
      if(!c){rows.push({line:n,source:`（${line.length} 字符）`,supports_udp:false,error:'无法识别的格式'});return;}
      host=c[1];port=+c[2];user=c[3]??'';
    }
    if(port<1||port>65535){rows.push({line:n,source:mask(protocol.toLowerCase()+'://'),supports_udp:false,error:'端口不合法'});return;}
    if(privateHost(host)){rows.push({line:n,source:mask(protocol.toLowerCase()+'://'),supports_udp:false,error:'出口地址是本机或内网地址，用户设备无法连接'});return;}
    const key=`${protocol}|${host}|${port}|${user}`;
    const src=`${protocol.toLowerCase()}://${user?user.slice(0,2)+'***@':''}${host}:${port}`;
    if(seen.has(key)){rows.push({line:n,source:src,supports_udp:false,duplicate:true,error:`与第 ${seen.get(key)} 行重复`});return;}
    seen.set(key,n);
    const row:PreviewRow={line:n,source:src,protocol,host,port,name:`${o.name_prefix||host}`,country_code:o.country_code,supports_udp:udp,existing_id:existing.get(key)};
    rows.push(row);valid.push({row,key});
  });
  return {rows,valid};
}

export function makePreviewEgress(_session:AdminSession):EgressSource{
  let seq=3;
  const keys=new Map<string,string>();
  const items:EgressItem[]=[
    {id:'e1',name:'新加坡 · 住宅 01',protocol:'SOCKS5',host:'203.0.113.10',port:1080,country_code:'SG',city:'新加坡',region:'ASIA',kind:'PROXY',supports_udp:true,identity_hint:'al***',params:{source_kind:'IMPORT'},min_tier:1,status:'ENABLED',sort_order:0,source:'IMPORT',subscription_id:null,created_at:now(),updated_at:now()},
    {id:'e2',name:'洛杉矶 · 机房 01',protocol:'HTTP',host:'198.51.100.21',port:3128,country_code:'US',city:'洛杉矶',region:'AMERICAS',kind:'PROXY',supports_udp:false,identity_hint:'bo***',params:{},min_tier:1,status:'ENABLED',sort_order:0,source:'IMPORT',subscription_id:null,created_at:now(),updated_at:now()},
    {id:'e3',name:'东京 · 自建 Reality',protocol:'VLESS',host:'jp1.example.com',port:443,country_code:'JP',city:'东京',region:'ASIA',kind:'NODE',supports_udp:true,identity_hint:'1f***',params:{security:'reality',sni:'www.example.com',flow:'xtls-rprx-vision'},min_tier:2,status:'DISABLED',sort_order:0,source:'IMPORT',subscription_id:null,created_at:now(),updated_at:now()},
  ];
  items.forEach(i=>keys.set(`${i.protocol}|${i.host}|${i.port}|${i.identity_hint.slice(0,2)}`,i.id));
  const subs:Subscription[]=[{id:'s1',name:'供应商 A（示例）',url_hint:'sub.example.com',default_protocol:'SOCKS5',country_code:'SG',region:'ASIA',min_tier:1,status:'ACTIVE',last_fetched_at:null,last_result:'尚未抓取（演示）'}];
  const done=new Map<string,unknown>();
  const once=<T,>(op:string,fn:()=>T):T=>{if(done.has(op))return done.get(op) as T;const v=fn();done.set(op,v);return v;};
  const wait=()=>new Promise(r=>setTimeout(r,250));
  const preview=(text:string,o:ImportOptions):PreviewResult=>{
    const {rows}=parse(text,o,keys);
    const valid=rows.filter(r=>!r.error).length,dup=rows.filter(r=>r.duplicate).length;
    return {rows,summary:{total:rows.length,valid,invalid:rows.length-valid-dup,duplicate:dup,existing:rows.filter(r=>r.existing_id).length}};
  };
  return {
    list:async q=>{await wait();let list=items.filter(i=>(!q.status||i.status===q.status)&&(!q.protocol||i.protocol===q.protocol)&&(!q.country_code||i.country_code===q.country_code)&&(!q.search||i.name.includes(q.search)||i.host.includes(q.search)));
      const total=list.length;list=list.slice((q.page-1)*q.page_size,q.page*q.page_size);return {items:structuredClone(list),total:String(total),page:q.page,page_size:q.page_size,observed_at:now()};},
    preview:async(text,o)=>{await wait();return preview(text,o);},
    importNow:async(op,text,o)=>{await wait();return once(op,():ImportResult=>{
      const {rows,valid}=parse(text,o,keys);const ids:string[]=[];let created=0,updated=0,skipped=0;
      for(const {row,key} of valid){
        const found=keys.get(key);
        if(found){if(o.update_existing){updated++;ids.push(found);}else skipped++;continue;}
        const id=`e${++seq}`;keys.set(key,id);ids.push(id);created++;
        items.push({id,name:row.name||row.host!,protocol:row.protocol!,host:row.host!,port:row.port!,country_code:o.country_code,city:o.city,region:o.region,kind:'PROXY',supports_udp:row.supports_udp,identity_hint:'**',params:{},min_tier:o.min_tier,status:o.enable?'ENABLED':'DISABLED',sort_order:0,source:'IMPORT',subscription_id:null,created_at:now(),updated_at:now()});
      }
      const failed=rows.filter(r=>r.error).length;
      return {created,updated,skipped,failed,disabled:0,ids,rows:rows.filter(r=>r.error).map(r=>({line:r.line,source:r.source,error:r.error}))};
    });},
    update:async(op,id,change)=>{await wait();return once(op,()=>{const it=items.find(i=>i.id===id);if(!it)throw new Error('出口不存在');
      if(change.supports_udp!==undefined&&it.protocol!=='SOCKS5')throw new Error('只有 SOCKS5 可以修改 UDP 能力');
      Object.assign(it,change,{updated_at:now()});return structuredClone(it);});},
    subscriptions:async()=>{await wait();return structuredClone(subs);},
    previewSubscription:async()=>{await wait();return preview('socks5://u1:p1@203.0.113.30:1080\nsocks5://u2:p2@203.0.113.31:1080\n10.0.0.8:1080:u3:p3',{default_protocol:'SOCKS5',default_udp:false,country_code:'SG',city:'',region:'ASIA',name_prefix:'订阅',min_tier:1,enable:false,update_existing:true});},
    createSubscription:async(op,input)=>{await wait();return once(op,()=>{const s:Subscription={id:`s${subs.length+1}`,name:input.name,url_hint:(()=>{try{return new URL(input.url).hostname;}catch{return '';}})(),default_protocol:input.default_protocol,country_code:input.country_code,region:input.region,min_tier:input.min_tier,status:'ACTIVE',last_fetched_at:null,last_result:'尚未抓取（演示）'};subs.push(s);return structuredClone(s);});},
    refreshSubscription:async(op,id)=>{await wait();return once(op,()=>{const s=subs.find(x=>x.id===id);if(!s)throw new Error('订阅不存在');s.last_fetched_at=now();s.last_result='演示：解析 3 行，2 行可用，1 行为内网地址被拒绝';return {created:0,updated:2,skipped:0,failed:1,disabled:0,ids:[],rows:[{line:3,source:'（18 字符）',error:'出口地址是本机或内网地址，用户设备无法连接'}]};});},
  };
}
