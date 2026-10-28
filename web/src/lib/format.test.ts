// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 从上游 internal/panel/frontend_test.go 的 TestAppJSCreditDimensionFormatting 平移（期望值原样保留）：
// 积分扣除维度的格式要稳定，缺样本 / 缺匹配 Token 时不能伪造比例。
import { describe, expect, it } from 'vitest'
import { dayClock, fmtBytes, fmtCredit, fmtCreditRatio, fmtModelRate } from './format'

describe('积分格式', () => {
  it('积分、积分 / 1M Token、倍率', () => {
    expect(fmtCredit(1.25)).toBe('1.25')
    expect(fmtCredit(0)).toBe('0')
    expect(fmtCredit(100)).toBe('100')
    expect(fmtCreditRatio(12.5, 2, 400)).toBe('12.5 / 1M')
    expect(fmtCreditRatio(12.5, 0, 400)).toBe('—')
    expect(fmtCreditRatio(12.5, 2, 0)).toBe('—')
    expect(fmtModelRate('0.5')).toBe('x0.5')
    expect(fmtModelRate('')).toBe('—')
  })
  it('字节', () => {
    expect(fmtBytes(512)).toBe('512 B')
    expect(fmtBytes(2048)).toBe('2.0 KB')
    expect(fmtBytes(3 * 1024 * 1024)).toBe('3.0 MB')
  })
  it('今天只显示时刻，更早的带日期', () => {
    const now = new Date(2026, 9, 28, 15, 0, 0)
    expect(dayClock(new Date(2026, 9, 28, 9, 5, 7).toISOString(), now)).toBe('09:05:07')
    expect(dayClock(new Date(2026, 9, 3, 23, 59, 1).toISOString(), now)).toBe('10-03 23:59:01')
    expect(dayClock('', now)).toBe('')
    expect(dayClock(undefined, now)).toBe('')
  })
})
