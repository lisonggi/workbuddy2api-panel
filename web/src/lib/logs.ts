// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 运行日志的频道与级别判定（级别靠关键字，与旧版一致；后端查历史时 matchLevel 同口径）。
// 1.5.0 起对话请求的流水行不进运行日志（看「请求记录」页），频道只剩任务和系统。
import { rangeQuery, type TimeRange } from './timerange'
import type { LogEntry } from './types'

export const LOG_CHANNELS = ['all', 'task', 'sys'] as const
export type LogChannel = (typeof LOG_CHANNELS)[number]
export const CHANNEL_LABEL: Record<string, string> = { all: '全部', task: '任务', sys: '系统' }

export const isLogChannel = (v: unknown): v is LogChannel => LOG_CHANNELS.includes(v as LogChannel)

/** 旧的「运行日志 → 对话」链接（?ch=chat），打开时转到请求记录页 */
export const isLegacyChatLink = (search: unknown): boolean =>
  typeof search === 'object' && search !== null && (search as { ch?: unknown }).ch === 'chat'

export type LogLevel = 'error' | 'warn' | 'info'
export const LEVEL_FILTERS = ['all', 'warn', 'error'] as const
export type LevelFilter = (typeof LEVEL_FILTERS)[number]

export function logLevel(text: string): LogLevel {
  if (/error|失败|错误/.test(text)) return 'error'
  if (/warn|冷却|熔断/.test(text)) return 'warn'
  return 'info'
}

/** 按频道、级别（警告及以上 / 仅错误）、关键字（不分大小写）筛选 */
export function filterLogs(entries: LogEntry[], ch: LogChannel, level: LevelFilter, q: string): LogEntry[] {
  const kw = q.trim().toLowerCase()
  return entries.filter((e) => {
    if (ch !== 'all' && e.ch !== ch) return false
    if (level !== 'all') {
      const lv = logLevel(e.text)
      if (level === 'error' ? lv !== 'error' : lv === 'info') return false
    }
    return !kw || e.text.toLowerCase().includes(kw)
  })
}

/** 按时间范围查历史一次读多少行（后端最多 5000） */
export const ARCHIVE_LIMITS = [500, 2000, 5000] as const

/**
 * 按时间范围查历史运行日志的查询参数：区间按线性日志口径一律折算成 from/to（「全部历史」不发），
 * 频道、级别、关键字交给后端在留存里筛，再取最新的 limit 行。
 */
export function logArchiveQuery(range: TimeRange, ch: LogChannel, level: LevelFilter, q: string, limit: number, now = Date.now()): URLSearchParams {
  const p = rangeQuery(range, false, now)
  if (ch !== 'all') p.set('ch', ch)
  if (level !== 'all') p.set('level', level)
  const kw = q.trim()
  if (kw) p.set('q', kw)
  p.set('limit', String(limit))
  return p
}
