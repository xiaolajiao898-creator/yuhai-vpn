import {moneyMicro, signedLedgerInteger, type EntitlementChangeType, type EntitlementSourceType, type BalanceEntryType, type BalanceSourceType, type UserStatus} from '../api/users.ts';

export const userStatusLabels:Record<UserStatus,string>={ACTIVE:'正常',SUSPENDED:'已暂停',BANNED:'已封禁'};
export const entryTypeLabels:Record<BalanceEntryType,string>={DEPOSIT:'充值到账',PURCHASE:'套餐购买',INVITE_REWARD:'邀请奖励',EXCEPTION_CREDIT:'异常到账补记',FULFILL_FALLBACK:'余额退回',ADMIN_CREDIT:'人工入账',ADMIN_DEBIT:'人工扣款'};
export const sourceTypeLabels:Record<BalanceSourceType,string>={CHAIN_TRANSFER:'链上转账',ORDER:'订单',INVITE_REWARD:'邀请奖励',DEPOSIT_EXCEPTION:'异常到账',ADMIN_APPROVAL:'管理审批'};
export const operatorLabels={USER:'用户',ADMIN:'管理员',SYSTEM:'系统'};
export function formatUSDT(value:string|null,showSign=false):string {
  if(value===null)return '—';
  const micro=moneyMicro(value,showSign);if(micro===null)return '—';
  const trimmed=value.replace(/0+$/,'').replace(/\.$/,'');
  return showSign&&micro>0n?`+${trimmed}`:trimmed;
}
export function userTime(value:string|null):string {return value===null?'未设置':new Date(value).toLocaleString('zh-CN',{timeZone:'Asia/Shanghai',hour12:false});}
export function formatBytes(value:string,binaryLabels=false):string {
  if(!/^(0|[1-9][0-9]*)$/.test(value))return '—';
  const bytes=BigInt(value),units:[[bigint,string],[bigint,string],[bigint,string]]=[[1073741824n,binaryLabels?'GiB':'GB'],[1048576n,binaryLabels?'MiB':'MB'],[1024n,binaryLabels?'KiB':'KB']];
  for(const [size,label] of units){if(bytes>=size){const hundredths=(bytes*100n+size/2n)/size;return `${hundredths/100n}${hundredths%100n?`.${String(hundredths%100n).padStart(2,'0').replace(/0$/,'')}`:''} ${label}`;}}
  return `${value} B`;
}
export function userPageTotal(total:string,size:number):number {
  const actual=BigInt(total),maximum=BigInt(10000*size);return Number(actual>maximum?maximum:actual);
}
export function userReadError(error:unknown):string {
  const status=typeof error==='object'&&error!==null&&'status' in error?error.status:undefined;
  if(status===403)return '当前账号没有查看这些资料的权限，请联系管理员';
  if(status===401)return '登录状态已失效，请重新登录后台';
  if(status===404)return '未找到该账户，请返回列表重新查询';
  return error instanceof Error?error.message:'暂时无法读取资料，请重试';
}

export const entitlementTypeLabels:Record<EntitlementChangeType,string>={PURCHASE:'开通套餐',RENEW:'续费',TIER_CHANGE:'套餐等级变更',TRAFFIC_PACK:'流量包',DEVICE_ADDON:'设备扩展',INVITE_REWARD:'邀请奖励',ADMIN_ADJUST:'人工调整',TRAFFIC_CONSUME_OVERFLOW:'超额流量扣减',PERIOD_RESET:'周期重置',EXPIRE:'权益到期',REVOKE:'权益撤销'};
export const entitlementSourceLabels:Record<EntitlementSourceType,string>={ORDER:'订单',INVITE_REWARD:'邀请奖励',ADMIN_APPROVAL:'管理审批',SYSTEM:'系统'};
export type EntitlementUnit='seconds'|'bytes'|'devices';
export function entitlementDelta(value:unknown,unit:EntitlementUnit):{label:string;exact:string|null;direction:'positive'|'negative'|'zero'|'missing'} {
  const n=signedLedgerInteger(value);
  if(n===null||(unit==='devices'&&(n < -2147483648n||n > 2147483647n)))return {label:'未记录',exact:null,direction:'missing'};
  const magnitude=n<0n?-n:n,sign=n>0n?'+':n<0n?'-':'',direction=n>0n?'positive':n<0n?'negative':'zero';
  if(unit==='devices')return {label:`${sign}${magnitude} 台`,exact:null,direction};
  if(unit==='bytes'){
    const size=magnitude>=1073741824n?1073741824n:magnitude>=1048576n?1048576n:magnitude>=1024n?1024n:1n;
    return {label:`${size>1n&&magnitude%size!==0n?'约 ':''}${sign}${formatBytes(String(magnitude),true)}`,exact:size>1n?`${sign}${magnitude} 字节`:null,direction};
  }
  const parts:string[]=[];let remaining=magnitude;
  for(const [scale,label] of [[86400n,'天'],[3600n,'小时'],[60n,'分'],[1n,'秒']] as const){const amount=remaining/scale;remaining%=scale;if(amount)parts.push(`${amount} ${label}`);}
  return {label:`${sign}${parts.length?parts.join(' '):'0 秒'}`,exact:magnitude>=60n?`${sign}${magnitude} 秒`:null,direction};
}
