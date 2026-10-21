// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

import { describe, expect, it } from 'vitest'
import { credentialFile } from './credentials'

const doc = (uid: string) => ({ auth: { accessToken: 'at-' + uid, refreshToken: 'rt' }, account: { uid, enterpriseId: '' } })

describe('credentialFile', () => {
  it('单个账号是一个对象，文件名同数据目录里的凭证文件', () => {
    const f = credentialFile([doc('u1')], new Date(2026, 9, 19, 15, 30))
    expect(f.name).toBe('workbuddy-u1.json')
    expect(JSON.parse(f.text)).toEqual(doc('u1'))
  })
  it('多个账号是一个数组，文件名带本地时间和个数', () => {
    const f = credentialFile([doc('u1'), doc('u2')], new Date(2026, 9, 19, 9, 5))
    expect(f.name).toBe('workbuddy-accounts-20261019-0905-2.json')
    expect(JSON.parse(f.text)).toEqual([doc('u1'), doc('u2')])
  })
})
