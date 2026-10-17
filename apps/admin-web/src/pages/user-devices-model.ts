import type {UserDevice} from '../api/user-devices.ts';
import {userTime} from './users-model.ts';

export const deviceStatusLabels={ACTIVE:'已绑定',REVOKED:'已撤销'};
export function deviceName(value:UserDevice):string {return value.display_name?.trim()||'未命名设备';}
export function deviceSeen(value:UserDevice):string {return value.last_seen_at===null?'暂无活跃记录':userTime(value.last_seen_at);}
export function deviceRevoked(value:UserDevice):string {return value.revoked_at===null?(value.status==='ACTIVE'?'未撤销':'暂无撤销时间'):userTime(value.revoked_at);}
