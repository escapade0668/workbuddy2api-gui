// realm.ts 域（国际版 / 国内版）判定的单一出处。
//
// 两个页面（模型与倍率、积分到期）都要按域分栏，判定口径必须一致：
// 模型靠后端给的 realm 字段（按取目录所用账号的域判定），账号靠 domain（workbuddy.ai = 国际版）。
import type { Account, Model } from './types'

export type Realm = 'global' | 'cn'

export const REALMS: { key: Realm; label: string; upstream: string }[] = [
  { key: 'global', label: '国际版', upstream: 'workbuddy.ai' },
  { key: 'cn', label: '国内版', upstream: 'codebuddy.cn' },
]

/** 模型所属域：以后端给的 realm 为准。
 *
 *  不能只看 id 前缀：网关（linbeize/workbuddy2api）返回的是不带前缀的裸 id，
 *  按前缀判会把国际版模型全归到国内版。仅在旧后端没给 realm 时才退回前缀判定。 */
export function modelRealm(m: Pick<Model, 'id' | 'realm'>): Realm {
  if (m.realm === 'global' || m.realm === 'cn') return m.realm
  return m.id.startsWith('global:') ? 'global' : 'cn'
}

/** 去掉域前缀，得到调用上游时真正使用的裸名。 */
export function bareID(id: string): string {
  return id.replace(/^(global|cn):/, '')
}

/** 账号域判定：domain 含 workbuddy.ai 为国际版，其余（copilot.tencent.com 等）为国内版。 */
export function accountRealm(a: Account): Realm {
  return (a.domain || '').toLowerCase().includes('workbuddy.ai') ? 'global' : 'cn'
}

/** 域的中文名，找不到返回原值。 */
export function realmLabel(r: Realm): string {
  return REALMS.find((x) => x.key === r)?.label ?? r
}
