// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// Recharts 自定义提示框收到的参数（只用到这两项；写成宽类型，免得和 Recharts 的泛型参数较劲）
export interface TipProps {
  active?: boolean
  payload?: readonly { payload?: unknown }[]
}
