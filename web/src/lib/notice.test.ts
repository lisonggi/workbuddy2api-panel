// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

import { describe, expect, it, vi } from 'vitest'
import { NOTICE_URL, REPO_URL, consoleNotice, printConsoleNotice } from './notice'

describe('控制台声明', () => {
  it('写明仓库、许可、署名要求和上游', () => {
    const text = consoleNotice().join('\n')
    expect(text).toContain(REPO_URL)
    expect(text).toContain('AGPL-3.0-only')
    expect(text).toContain(NOTICE_URL)
    expect(text).toContain('注明出处')
    expect(text).toContain('linguo2625469/workbuddy2api-panel')
    expect(text).toContain('不提供任何担保')
  })

  it('标题加粗，其余几行跟在后面，只打一次', () => {
    const log = vi.fn()
    printConsoleNotice(log)
    expect(log).toHaveBeenCalledTimes(1)
    const [fmt, titleStyle, bodyStyle] = log.mock.calls[0] as string[]
    expect(fmt.startsWith('%cworkbuddy2api-panel · ' + REPO_URL + '\n%c')).toBe(true)
    expect(fmt).toContain('AGPL-3.0-only')
    expect(titleStyle).toContain('font-weight:600')
    expect(bodyStyle).toContain('font-weight:400')
  })
})
