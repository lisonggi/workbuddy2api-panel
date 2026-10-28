// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 运行日志页：网关的系统与任务日志，按频道 / 级别 / 关键字筛选。两种看法：
//   - 实时：最近 500 行（网关内存环形缓冲）。停在底部时每 5 秒跟随新日志；往上翻看旧日志时冻结画面
//     （日志环满了会从顶部淘汰旧行，不冻结的话翻看中的内容会整体上移），滚回底部或点「回到最新」再继续跟随。
//   - 历史：按时间范围从磁盘上的任务日志、系统日志留存里查（配置 →「日志留存」），频道、级别、关键字在网关侧筛，
//     取区间内最新的 N 行。
// 每次调用 API 的记录在「请求记录」页，对话流水行不进这里。
import { useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { Button, Chip, Label, ListBox, SearchField, Select, Surface, ToggleButton, ToggleButtonGroup } from '@heroui/react'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { dayClock } from '../../lib/format'
import { useDebounced, useStoredChoice } from '../../lib/hooks'
import { ARCHIVE_LIMITS, CHANNEL_LABEL, LOG_CHANNELS, filterLogs, logLevel, type LevelFilter, type LogChannel } from '../../lib/logs'
import { useLogArchive, useLogStorage, useLogs } from '../../lib/queries'
import { STORE_LABEL } from '../../lib/storage'
import { initialRange, rangeLabel } from '../../lib/timerange'
import type { LogEntry } from '../../lib/types'
import { BusyButton, Empty, Loaded } from '../../components/Feedback'
import { Panel } from '../../components/Panel'
import { TimeRangePicker } from '../../components/TimeRangePicker'

const CH_COLOR: Record<string, 'accent' | 'default'> = { task: 'accent', sys: 'default' }
const LEVEL_TEXT = { error: 'text-danger', warn: 'text-warning', info: '' }
const MODES = ['live', 'history'] as const
type Mode = (typeof MODES)[number]
/** 频道对应的留存（日志留存里的叫法） */
const CH_STORE = { task: 'task', sys: 'system' } as const

/** 冻结后又来了多少行（日志环满 500 行后总数不变，所以按冻结时最后一行的位置算） */
function newerThan(live: LogEntry[], frozen: LogEntry[]): number {
  const last = frozen[frozen.length - 1]
  if (!last) return live.length
  for (let i = live.length - 1; i >= 0; i--) {
    if (live[i].ts === last.ts && live[i].text === last.text) return live.length - 1 - i
  }
  return live.length
}

/** 两种看法共用的条件：频道在地址栏里（任务中心「查看日志」直接带过来），级别和关键字在页面里 */
interface ViewProps {
  ch: LogChannel
  level: LevelFilter
  q: string
  /** 频道、级别、关键字三个控件 */
  filters: ReactNode
  /** 实时 / 历史切换 */
  modeSwitch: ReactNode
}

export function LogsPage() {
  const { ch = 'all' } = useSearch({ from: '/logs' })
  const navigate = useNavigate({ from: '/logs' })
  const [mode, setMode] = useStoredChoice<Mode>('wb2api.logsMode', MODES, 'live')
  const [level, setLevel] = useState<LevelFilter>('all')
  const [q, setQ] = useState('')

  const filters = (
    <>
      <ToggleButtonGroup aria-label="频道" size="sm" selectionMode="single" disallowEmptySelection
        selectedKeys={[ch]} onSelectionChange={(k) => void navigate({ search: { ch: [...k][0] as LogChannel }, replace: true })}>
        {LOG_CHANNELS.map((c, i) => (
          <ToggleButton key={c} id={c}>{i > 0 && <ToggleButtonGroup.Separator />}{CHANNEL_LABEL[c]}</ToggleButton>
        ))}
      </ToggleButtonGroup>
      <ToggleButtonGroup aria-label="级别" size="sm" selectionMode="single" disallowEmptySelection
        selectedKeys={[level]} onSelectionChange={(k) => setLevel([...k][0] as LevelFilter)}>
        <ToggleButton id="all">全部级别</ToggleButton>
        <ToggleButton id="warn"><ToggleButtonGroup.Separator />警告及错误</ToggleButton>
        <ToggleButton id="error"><ToggleButtonGroup.Separator />仅错误</ToggleButton>
      </ToggleButtonGroup>
      <SearchField aria-label="搜索日志" value={q} onChange={setQ} className="w-full sm:w-64">
        <SearchField.Group>
          <SearchField.SearchIcon />
          <SearchField.Input placeholder="搜索日志内容" />
          <SearchField.ClearButton />
        </SearchField.Group>
      </SearchField>
    </>
  )
  const modeSwitch = (
    <ToggleButtonGroup aria-label="看法" size="sm" selectionMode="single" disallowEmptySelection
      selectedKeys={[mode]} onSelectionChange={(k) => setMode([...k][0] as Mode)}>
      <ToggleButton id="live">实时</ToggleButton>
      <ToggleButton id="history"><ToggleButtonGroup.Separator />历史</ToggleButton>
    </ToggleButtonGroup>
  )
  const props = { ch, level, q, filters, modeSwitch }
  return mode === 'history' ? <HistoryLogs {...props} /> : <LiveLogs {...props} />
}

/** 日志行：全部频道时行首标出频道；今天的只显示时刻，更早的带日期 */
function Lines({ entries, ch }: { entries: LogEntry[]; ch: LogChannel }) {
  return entries.map((e, i) => (
    <div key={i} className={`whitespace-pre-wrap break-all ${LEVEL_TEXT[logLevel(e.text)]}`}>
      {ch === 'all' && <Chip size="sm" variant="soft" color={CH_COLOR[e.ch] ?? 'default'} className="mr-2 align-middle">{CHANNEL_LABEL[e.ch] ?? e.ch}</Chip>}
      <span className="text-muted">{dayClock(e.ts)}</span> {e.text}
    </div>
  ))
}

const BOX = 'h-[60dvh] min-h-72 overflow-auto rounded-xl p-3 font-mono text-xs leading-6'

function LiveLogs({ ch, level, q, filters, modeSwitch }: ViewProps) {
  const [following, setFollowing] = useState(true)
  const [frozen, setFrozen] = useState<LogEntry[] | null>(null)
  const query = useLogs(true)
  const box = useRef<HTMLDivElement>(null)

  const live = useMemo(() => query.data?.entries || [], [query.data])
  const entries = following ? live : (frozen ?? live)
  const shown = useMemo(() => filterLogs(entries, ch, level, q), [entries, ch, level, q])
  const newer = following || !frozen ? 0 : newerThan(live, frozen)
  const counts = useMemo(() => {
    const c: Record<string, number> = {}
    for (const e of live) c[e.ch] = (c[e.ch] || 0) + 1
    return c
  }, [live])

  // 跟随时新内容渲染后贴底
  useLayoutEffect(() => {
    if (following && box.current) box.current.scrollTop = box.current.scrollHeight
  }, [shown, following])

  const onScroll = () => {
    const el = box.current
    if (!el) return
    const atEnd = el.scrollTop + el.clientHeight >= el.scrollHeight - 24
    if (atEnd && !following) {
      setFollowing(true)
      setFrozen(null)
    } else if (!atEnd && following) {
      setFollowing(false)
      setFrozen(live)
    }
  }

  const backToLatest = () => {
    setFrozen(null)
    setFollowing(true)
  }

  return (
    <Panel
      title="运行日志"
      desc={ch === 'all'
        ? `最近 ${live.length} 行 · 任务 ${counts.task || 0} · 系统 ${counts.sys || 0}`
        : `${CHANNEL_LABEL[ch]} ${counts[ch] || 0} 行`}
      actions={modeSwitch}
      contentClassName="flex flex-col gap-3"
    >
      <div className="flex flex-wrap items-center gap-3">
        {filters}
        {!following && (
          <Button size="sm" variant="primary" className="sm:ml-auto" onPress={backToLatest}>
            回到最新{newer > 0 ? `（${newer} 行新日志）` : ''}
          </Button>
        )}
      </div>
      <Loaded query={query} rows={8}>
        {() => (
          <Surface ref={box} onScroll={onScroll} variant="secondary" className={BOX}>
            {shown.length === 0 ? <Empty title={live.length ? '没有符合条件的日志' : '暂无日志'} /> : <Lines entries={shown} ch={ch} />}
          </Surface>
        )}
      </Loaded>
    </Panel>
  )
}

function HistoryLogs({ ch, level, q, filters, modeSwitch }: ViewProps) {
  const [range, setRange] = useState(() => initialRange('today'))
  const [limit, setLimit] = useState<number>(ARCHIVE_LIMITS[0])
  const kw = useDebounced(q)
  const query = useLogArchive(range, ch, level, kw, limit)
  const storage = useLogStorage(true)
  const box = useRef<HTMLDivElement>(null)

  const entries = useMemo(() => query.data?.entries || [], [query.data])
  // 关键字停下来才交给网关查；这期间先在已读到的行里筛，打字时画面跟得上
  const shown = useMemo(() => filterLogs(entries, ch, level, q), [entries, ch, level, q])
  const more = !!query.data?.more
  // 留存关着的那类只查得到关闭前留下的文件：说一声，免得以为没日志
  const off = (ch === 'all' ? Object.values(CH_STORE) : [CH_STORE[ch]]).filter((k) => storage.data?.[k]?.enabled === false)

  // 和实时一样最新的在最下面：读到新结果后贴底
  useLayoutEffect(() => {
    if (box.current) box.current.scrollTop = box.current.scrollHeight
  }, [shown])

  return (
    <Panel
      title="运行日志"
      desc={`${rangeLabel(range)} · ${query.data ? entries.length + ' 行' : '读取中'}${more ? ` · 区间内不止 ${limit} 行，只显示最新的` : ''}`}
      actions={<>{modeSwitch}<BusyButton size="sm" variant="secondary" busy={query.isFetching} onPress={() => void query.refetch()}>重新读取</BusyButton></>}
      contentClassName="flex flex-col gap-3"
    >
      <div className="flex flex-wrap items-center gap-3">
        {filters}
        <TimeRangePicker value={range} onChange={setRange} />
        <Select aria-label="读取行数" value={limit} onChange={(k) => k != null && setLimit(Number(k))} className="w-36">
          <Label className="sr-only">读取行数</Label>
          <Select.Trigger><Select.Value /><Select.Indicator /></Select.Trigger>
          <Select.Popover>
            <ListBox>
              {ARCHIVE_LIMITS.map((n) => <ListBox.Item key={n} id={n} textValue={`最近 ${n} 行`}>最近 {n} 行<ListBox.ItemIndicator /></ListBox.Item>)}
            </ListBox>
          </Select.Popover>
        </Select>
      </div>
      {off.length > 0 && (
        <p className="text-sm text-warning">
          {off.map((k) => STORE_LABEL[k]).join('、')}的留存已关闭，这里只查得到关闭前留下的；在「配置 → 日志留存」里打开。
        </p>
      )}
      <Loaded query={query} rows={8}>
        {() => (
          <Surface ref={box} variant="secondary" className={BOX}>
            {more && <div className="text-muted">…… 更早的没有显示：缩小时间范围或加大读取行数</div>}
            {shown.length === 0 ? <Empty title="这段时间没有符合条件的日志" /> : <Lines entries={shown} ch={ch} />}
          </Surface>
        )}
      </Loaded>
    </Panel>
  )
}
