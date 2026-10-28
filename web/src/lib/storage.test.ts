// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

import { describe, expect, it } from 'vitest'
import { retentionText, storageProblem, storageSummary } from './storage'

describe('日志留存', () => {
  it('保留天数：0 = 永久保留', () => {
    expect(retentionText(7)).toBe('保留 7 天')
    expect(retentionText(0)).toBe('永久保留')
    expect(retentionText(undefined)).toBe('永久保留')
  })

  it('占用：文件数、大小、最早日期', () => {
    expect(storageSummary('task', { enabled: true, files: 3, bytes: 3 * 1024 * 1024, oldest_day: '2026-10-21' })).toBe('3 个文件 · 3.0 MB · 最早 10-21')
    expect(storageSummary('system', { enabled: true, files: 0, bytes: 0 })).toBe('还没有记录')
    expect(storageSummary('requests', { enabled: false })).toBe('还没有记录')
    expect(storageSummary('usage', { retention_days: 0, buckets: 1234, bytes: 2048, oldest_day: '2026-09-01' })).toBe('1,234 条明细 · 2.0 KB · 最早 09-01')
    expect(storageSummary('usage', { retention_days: 0, buckets: 0, bytes: 0 })).toBe('还没有用量')
    expect(storageSummary('task', null)).toBe('当前网关不支持')
  })

  it('异常：写盘错误与丢弃条数，用量统计没有这两项', () => {
    expect(storageProblem({ enabled: true, last_error: 'disk full', dropped_writes: 2 })).toBe('错误：disk full · 丢弃 2 条')
    expect(storageProblem({ enabled: true })).toBe('')
    expect(storageProblem({ retention_days: 0, buckets: 1, bytes: 1 })).toBe('')
    expect(storageProblem(null)).toBe('')
  })
})
