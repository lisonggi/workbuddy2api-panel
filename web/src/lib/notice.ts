// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 许可与出处声明：「版本信息」窗口（AGPL-3.0 第 5 条 d 款的适当法律声明）和浏览器控制台共用。
// 条款原文在仓库的 LICENSE、NOTICE。

export const REPO_URL = 'https://github.com/yreidev/workbuddy2api-panel'
export const LICENSE_URL = REPO_URL + '/blob/main/LICENSE'
export const NOTICE_URL = REPO_URL + '/blob/main/NOTICE'
export const UPSTREAMS = [
  { name: 'Sliverkiss/workbuddy2api', url: 'https://github.com/Sliverkiss' },
  { name: 'linguo2625469/workbuddy2api-panel', url: 'https://github.com/linguo2625469/workbuddy2api-panel' },
]

/** 控制台里打印的几行：项目、许可、署名要求、上游、无担保 */
export function consoleNotice(): string[] {
  return [
    'workbuddy2api-panel · ' + REPO_URL,
    '以 AGPL-3.0-only 授权，附署名条款（见 ' + NOTICE_URL + '）。',
    '复制或改写本项目的代码须保留版权声明，在 README 和界面里注明出处并附上链接；修改版须标明修改，不得冒充原版。',
    '基于 ' + UPSTREAMS.map((u) => u.name).join(' 与 ') + '。本程序按原样提供，不提供任何担保。',
  ]
}

/** 页面加载时在浏览器控制台打一次声明 */
export function printConsoleNotice(log: (...args: unknown[]) => void = console.info) {
  const [title, ...rest] = consoleNotice()
  log('%c' + title + '\n%c' + rest.join('\n'), 'font-weight:600;font-size:13px', 'font-weight:400')
}
