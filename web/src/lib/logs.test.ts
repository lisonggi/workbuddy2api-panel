// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

import { describe, expect, it } from 'vitest'
import { filterLogs, isLegacyChatLink, logArchiveQuery, logLevel } from './logs'
import { initialRange } from './timerange'

const entries = [
  { ts: '', ch: 'task', text: '签到完成 uid=a' },
  { ts: '', ch: 'task', text: '签到失败 uid=b' },
  { ts: '', ch: 'sys', text: '限流冷却 600s' },
  { ts: '', ch: 'sys', text: 'Panel Started' },
]

describe('logs', () => {
  it('按关键字判定级别', () => {
    expect(logLevel('upstream error 502')).toBe('error')
    expect(logLevel('熔断 30m')).toBe('warn')
    expect(logLevel('ok')).toBe('info')
  })

  it('频道、级别、关键字组合筛选', () => {
    expect(filterLogs(entries, 'task', 'all', '').length).toBe(2)
    expect(filterLogs(entries, 'all', 'warn', '').map((e) => e.text)).toEqual(['签到失败 uid=b', '限流冷却 600s'])
    expect(filterLogs(entries, 'all', 'error', '').map((e) => e.text)).toEqual(['签到失败 uid=b'])
    expect(filterLogs(entries, 'all', 'all', 'panel').map((e) => e.text)).toEqual(['Panel Started'])
    expect(filterLogs(entries, 'sys', 'all', '').length).toBe(2)
  })

  it('旧的对话频道链接认得出来', () => {
    expect(isLegacyChatLink({ ch: 'chat' })).toBe(true)
    expect(isLegacyChatLink({ ch: 'task' })).toBe(false)
    expect(isLegacyChatLink({})).toBe(false)
    expect(isLegacyChatLink(undefined)).toBe(false)
  })

  it('查历史的参数：区间一律折算成 from/to，筛选条件交给网关', () => {
    const now = new Date(2026, 9, 28, 15, 0, 0).getTime()
    const today = logArchiveQuery({ preset: 'today', from: null, to: null }, 'all', 'all', '  ', 500, now)
    expect(Object.fromEntries(today)).toEqual({ from: String(new Date(2026, 9, 28).getTime() / 1000), limit: '500' })
    const week = logArchiveQuery({ preset: '168', from: null, to: null }, 'task', 'warn', ' 签到 ', 2000, now)
    expect(Object.fromEntries(week)).toEqual({ from: String((now - 168 * 3600_000) / 1000), ch: 'task', level: 'warn', q: '签到', limit: '2000' })
    const custom = initialRange('custom')
    const p = logArchiveQuery(custom, 'sys', 'error', '', 5000, now)
    expect(p.get('from')).toBe(String(Math.floor(custom.from!.getTime() / 1000)))
    expect(p.get('to')).toBe(String(Math.floor(custom.to!.getTime() / 1000)))
    expect(p.get('level')).toBe('error')
    expect(logArchiveQuery({ preset: '0', from: null, to: null }, 'all', 'all', '', 500, now).toString()).toBe('limit=500')
  })
})
