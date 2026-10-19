import type {AdminSession} from '../api/client.ts';
import {heartbeatAt,nodeQueryProblem,readNodeDetail,readNodePage,type ManagedNode,type NodeSource,type NodeStatus,type NodeDetail} from '../api/nodes.ts';

export function makePreviewNodes(session:AdminSession,now=()=>Date.now()):NodeSource {
  const created=now();
  const countries=[['HK','香港','ASIA'],['JP','东京','ASIA'],['SG','新加坡','ASIA'],['US','洛杉矶','AMERICAS'],['DE','法兰克福','EUROPE']];
  const nodes:ManagedNode[]=Array.from({length:26},(_,i)=>{
    const [country,city,region]=countries[i%5];
    const state:NodeStatus=(['ONLINE','ONLINE','OFFLINE','MAINTENANCE','DISABLED'] as const)[i%5];
    const last=i%6===0?null:new Date(created-(i%6===1?20:i%6===2?240:i%6===3?-30:60)*1000).toISOString();
    return {id:String(9007199254740993n+BigInt(i)),name:`${city} · ${String(i+1).padStart(2,'0')}${i===0?' 主线路':''}`,group:{id:String(i%2+1),name:i%2?'优选线路':'标准线路',min_tier:i%2+1},country_code:country,city,region,status:state,is_recommended:i<3,capacity_users:500,online_count:i%6===0?0:18+i*11,load_score:(i*17)%101,last_heartbeat_at:last,heartbeat_state:heartbeatAt(last,new Date(created).toISOString()),agent_version:i%6===0?null:'1.0.0',updated_at:new Date(created-3600000).toISOString()};
  });
  function guard(signal?:AbortSignal){if(signal?.aborted)throw new DOMException('请求已取消','AbortError');if(!session.permissions.includes('nodes.read'))throw Object.assign(new Error('没有节点查看权限'),{status:403});}
  function snapshot(n:ManagedNode,observed:string){return {...structuredClone(n),heartbeat_state:heartbeatAt(n.last_heartbeat_at,observed)};}
  return {
    list:async(q,signal)=>{
      guard(signal);const problem=nodeQueryProblem(q);if(problem)throw new Error(problem);
      const observed=new Date(now()).toISOString();
      const matches=nodes.filter(n=>(!q.search||n.name.toLowerCase().includes(q.search.toLowerCase()))&&(!q.status||n.status===q.status)&&(!q.country_code||n.country_code===q.country_code));
      return readNodePage({items:matches.slice((q.page-1)*q.page_size,q.page*q.page_size).map(n=>snapshot(n,observed)),total:String(matches.length),page:q.page,page_size:q.page_size,observed_at:observed},q);
    },
    get:async(id,signal)=>{
      guard(signal);const n=nodes.find(v=>v.id===id);if(!n)throw Object.assign(new Error('节点不存在'),{status:404});
      const observed=new Date(now()).toISOString();
      const result:NodeDetail={node:snapshot(n,observed),observed_at:observed,sync_status:'UNAVAILABLE',inbounds:[{id:String(BigInt(id)*2n),protocol:'VLESS_REALITY',port:443,priority:100,status:'ENABLED'},{id:String(BigInt(id)*2n+1n),protocol:'WIREGUARD',port:51820,priority:200,status:'DISABLED'}]};
      return readNodeDetail(result,id);
    },
    updateCapacity:async(id,capacity)=>{
      guard();
      if(!session.permissions.includes('nodes.write'))throw Object.assign(new Error('没有节点修改权限'),{status:403});
      if(!Number.isInteger(capacity)||capacity<0||capacity>1000000)throw new Error('容量应在 0–1000000，0 表示不按连接数判满');
      const n=nodes.find(v=>v.id===id);if(!n)throw Object.assign(new Error('节点不存在'),{status:404});
      n.capacity_users=capacity;n.updated_at=new Date(now()).toISOString();
      return readManagedNode(snapshot(n,n.updated_at),n.updated_at);
    },
  };
}
