// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 悬停提示：代替原生 title，用 HeroUI Tooltip（有淡入动画，键盘聚焦也能看到）。
// 没有提示内容时只渲染外层 div，调用方不用自己分两种情况写。
// HeroUI 提示框默认 break-all，英文错误消息会在单词中间断开；这里改成优先在空格处换行，UA 这类长串照样能断。
import type { CSSProperties, ReactNode } from 'react'
import { Tooltip } from '@heroui/react'

export function Hint({ tip, className, style, children }: {
  /** 提示内容；空串或 undefined = 不加提示 */
  tip?: ReactNode
  /** 外层（提示的触发区域）的样式，原来写在带 title 的元素上的 class 搬到这里 */
  className?: string
  style?: CSSProperties
  children?: ReactNode
}) {
  if (!tip) return <div className={className} style={style}>{children}</div>
  return (
    <Tooltip delay={300}>
      <Tooltip.Trigger className={className} style={style}>{children}</Tooltip.Trigger>
      <Tooltip.Content className="max-w-xs whitespace-pre-line break-normal wrap-anywhere">{tip}</Tooltip.Content>
    </Tooltip>
  )
}
