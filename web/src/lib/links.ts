// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

/** 完整地址（https://…、mailto:…、//host/…）算外链：不走面板内部路由，原样作为 href */
export const isExternalHref = (href: string) => /^([a-z][a-z\d+.-]*:|\/\/)/i.test(href)
