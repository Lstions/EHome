import { nextTick } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Delete, Download, VideoPause, VideoPlay } from '@element-plus/icons-vue'
import LogRealtimeViewer from '@/components/node/LogRealtimeViewer.vue'
import LogRealtimeViewerSource from '@/components/node/LogRealtimeViewer.vue?raw'

type LogLine = {
  id: number
  ts: number
  level: number
  tag: string
  msg: string
}

type SearchCountState = {
  epoch: number
  baselineId: number
  baselineMatchIds: number[]
  matchedAfterBaseline: number
}

function makeLogs(count: number, start = 0): LogLine[] {
  return Array.from({ length: count }, (_, index) => ({
    id: start + index + 1,
    ts: (start + index) * 1_000_000,
    level: (start + index) % 5,
    tag: `TAG_${start + index}`,
    msg: `message ${start + index}`,
  }))
}

function currentSearchCountState(
  logs: LogLine[],
  receivedCount: number,
  searchKeyword: string,
  epoch = 1,
): SearchCountState {
  const keyword = searchKeyword.trim().toLocaleLowerCase()
  const matches = logs.filter(line =>
    line.tag.toLocaleLowerCase().includes(keyword)
      || line.msg.toLocaleLowerCase().includes(keyword)
      || ['error', 'warn', 'info', 'debug', 'verbose'][line.level]?.includes(keyword),
  )
  return {
    epoch,
    baselineId: receivedCount,
    baselineMatchIds: matches.map(line => line.id),
    matchedAfterBaseline: 0,
  }
}

function mountViewer(props: {
  logs?: LogLine[]
  receivedCount?: number
  generation?: number
  paused?: boolean
  searchKeyword?: string
  searchCountState?: SearchCountState
} = {}): VueWrapper {
  const logs = props.logs ?? makeLogs(10)
  const receivedCount = props.receivedCount ?? logs.length
  const searchKeyword = props.searchKeyword ?? ''
  return mount(LogRealtimeViewer, {
    attachTo: document.body,
    props: {
      logs,
      receivedCount,
      generation: props.generation ?? 0,
      paused: props.paused ?? false,
      searchKeyword,
      searchCountState: props.searchCountState ?? (searchKeyword
        ? currentSearchCountState(logs, receivedCount, searchKeyword)
        : {
            epoch: 0,
            baselineId: receivedCount,
            baselineMatchIds: [],
            matchedAfterBaseline: 0,
          }),
    },
  })
}

describe('LogRealtimeViewer', () => {
  const wrappers: VueWrapper[] = []

  beforeEach(() => {
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(480)
    vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(900)
  })

  afterEach(() => {
    wrappers.splice(0).forEach(wrapper => wrapper.unmount())
    vi.restoreAllMocks()
  })

  const track = (wrapper: VueWrapper) => {
    wrappers.push(wrapper)
    return wrapper
  }

  it('uses the real fixed-row RecycleScroller and only mounts a bounded window for 5000 logs', async () => {
    const wrapper = track(mountViewer({ logs: makeLogs(5000) }))
    await nextTick()

    const scroller = wrapper.find('.log-scroller')
    expect(scroller.classes()).toContain('vue-recycle-scroller')
    expect(wrapper.findAll('.log-line').length).toBeGreaterThan(0)
    expect(wrapper.findAll('.log-line').length).toBeLessThan(100)
  })

  it('automatically follows a new log while the viewer is at the bottom', async () => {
    const initialLogs = makeLogs(30)
    const wrapper = track(mountViewer({ logs: initialLogs }))
    await nextTick()

    const scroller = wrapper.find('.log-scroller').element as HTMLElement
    expect(scroller.scrollTop).toBe(360)

    await wrapper.setProps({ logs: [...initialLogs, ...makeLogs(2, 30)], receivedCount: 32 })
    await nextTick()

    expect(scroller.scrollTop).toBe(416)
  })

  it('does not pull the user back after an upward scroll and reports unread logs', async () => {
    const initialLogs = makeLogs(30)
    const wrapper = track(mountViewer({ logs: initialLogs }))
    await nextTick()

    const scroller = wrapper.find('.log-scroller').element as HTMLElement
    scroller.scrollTop = 80
    await scroller.dispatchEvent(new Event('scroll'))

    await wrapper.setProps({ logs: [...initialLogs, ...makeLogs(3, 30)], receivedCount: 33 })
    await nextTick()

    expect(scroller.scrollTop).toBe(80)
    expect(wrapper.find('.unread-button').text()).toContain('3 条新日志')

    await wrapper.get('.unread-button').trigger('click')

    expect(scroller.scrollTop).toBe(444)
    expect(wrapper.find('.unread-button').exists()).toBe(false)
  })

  it('counts every received line across the 5000-line cap while scrolled up', async () => {
    const firstBatch = makeLogs(4900)
    const wrapper = track(mountViewer({ logs: firstBatch, receivedCount: 4900 }))
    await nextTick()

    const scroller = wrapper.get('.log-scroller').element as HTMLElement
    scroller.scrollTop = 80
    await scroller.dispatchEvent(new Event('scroll'))

    await wrapper.setProps({
      logs: [...firstBatch, ...makeLogs(200, 4900)].slice(-5000),
      receivedCount: 5100,
    })
    expect(wrapper.get('.unread-button').text()).toContain('200 条新日志')

    await wrapper.setProps({ logs: makeLogs(5000, 107), receivedCount: 5107 })
    expect(wrapper.get('.unread-button').text()).toContain('207 条新日志')
    expect(scroller.scrollTop).toBe(80)
  })

  it('reports all 6000 unread arrivals while paused even though only 5000 rows remain', async () => {
    const wrapper = track(mountViewer({ logs: [], receivedCount: 0, paused: true }))
    await nextTick()

    await wrapper.setProps({ logs: makeLogs(6000).slice(-5000), receivedCount: 6000 })

    expect(wrapper.get('.unread-button').text()).toContain('6000 条新日志')
  })

  it('reports all 6000 unread arrivals while manually scrolled up after trimming', async () => {
    const initialLogs = makeLogs(30)
    const wrapper = track(mountViewer({ logs: initialLogs, receivedCount: 30 }))
    await nextTick()

    const scroller = wrapper.get('.log-scroller').element as HTMLElement
    scroller.scrollTop = 80
    await scroller.dispatchEvent(new Event('scroll'))

    await wrapper.setProps({
      logs: makeLogs(6000, 30).slice(-5000),
      receivedCount: 6030,
    })

    expect(scroller.scrollTop).toBe(80)
    expect(wrapper.get('.unread-button').text()).toContain('6000 条新日志')
  })

  it('keeps following when the user remains close to the bottom', async () => {
    const initialLogs = makeLogs(30)
    const wrapper = track(mountViewer({ logs: initialLogs }))
    await nextTick()

    const scroller = wrapper.find('.log-scroller').element as HTMLElement
    scroller.scrollTop = 320
    scroller.dispatchEvent(new Event('scroll'))

    await wrapper.setProps({ logs: [...initialLogs, ...makeLogs(1, 30)], receivedCount: 31 })

    expect(scroller.scrollTop).toBe(388)
    expect(wrapper.find('.unread-button').exists()).toBe(false)
  })

  it('pauses only automatic following and retains logs received while paused', async () => {
    const initialLogs = makeLogs(30)
    const wrapper = track(mountViewer({ logs: initialLogs, paused: true }))
    await nextTick()

    const scroller = wrapper.find('.log-scroller').element as HTMLElement
    expect(scroller.scrollTop).toBe(360)

    await wrapper.setProps({ logs: [...initialLogs, ...makeLogs(2, 30)], receivedCount: 32 })

    expect(scroller.scrollTop).toBe(360)
    expect(wrapper.get('.log-summary').text()).toContain('32 / 32')
    expect(wrapper.get('.unread-button').text()).toContain('2 条新日志')

    await wrapper.get('.pause-button').trigger('click')
    expect(wrapper.emitted('update:paused')).toEqual([[false]])

    await wrapper.setProps({ paused: false })
    expect(scroller.scrollTop).toBe(416)
    expect(wrapper.find('.unread-button').exists()).toBe(false)
  })

  it('does not recount matching read logs when paused search establishes a new epoch', async () => {
    const initialLogs = makeLogs(30).map(line => ({ ...line, level: 2 }))
    const wrapper = track(mountViewer({ logs: initialLogs, receivedCount: 30, paused: true }))
    await nextTick()

    const updatedLogs = [
      ...initialLogs,
      { id: 31, ts: 31, level: 2, tag: 'NEW', msg: 'first unread info' },
      { id: 32, ts: 32, level: 2, tag: 'NEW', msg: 'second unread info' },
    ]
    await wrapper.setProps({ logs: updatedLogs, receivedCount: 32 })
    expect(wrapper.get('.unread-button').text()).toContain('2 条新日志')

    await wrapper.setProps({
      searchKeyword: 'info',
      searchCountState: currentSearchCountState(updatedLogs, 32, 'info', 1),
    })

    expect(wrapper.get('.unread-button').text()).toBe('2 条新日志 · 回到底部')
  })

  it('does not recount matching read logs when manual-scroll search establishes a new epoch', async () => {
    const initialLogs = makeLogs(30).map(line => ({ ...line, level: 2 }))
    const wrapper = track(mountViewer({ logs: initialLogs, receivedCount: 30 }))
    await nextTick()

    const scroller = wrapper.get('.log-scroller').element as HTMLElement
    scroller.scrollTop = 80
    await scroller.dispatchEvent(new Event('scroll'))

    const updatedLogs = [
      ...initialLogs,
      { id: 31, ts: 31, level: 2, tag: 'NEW', msg: 'first unread info' },
      { id: 32, ts: 32, level: 2, tag: 'NEW', msg: 'second unread info' },
    ]
    await wrapper.setProps({ logs: updatedLogs, receivedCount: 32 })
    expect(wrapper.get('.unread-button').text()).toContain('2 条新日志')

    await wrapper.setProps({
      searchKeyword: 'info',
      searchCountState: currentSearchCountState(updatedLogs, 32, 'info', 1),
    })

    expect(scroller.scrollTop).toBe(80)
    expect(wrapper.get('.unread-button').text()).toBe('2 条新日志 · 回到底部')
  })

  it('recomputes paused unread logs from the stable read watermark when search changes', async () => {
    const initialLogs = makeLogs(30)
    const wrapper = track(mountViewer({ logs: initialLogs, paused: true }))
    await nextTick()

    await wrapper.setProps({
      logs: [
        ...initialLogs,
        { id: 31, ts: 31, level: 2, tag: 'KEEP', msg: 'first match' },
        { id: 32, ts: 32, level: 2, tag: 'DROP', msg: 'other unread' },
        { id: 33, ts: 33, level: 2, tag: 'KEEP', msg: 'second match' },
      ],
      receivedCount: 33,
    })
    expect(wrapper.get('.unread-button').text()).toContain('3 条新日志')

    await wrapper.setProps({
      searchKeyword: 'keep',
      searchCountState: currentSearchCountState([
        ...initialLogs,
        { id: 31, ts: 31, level: 2, tag: 'KEEP', msg: 'first match' },
        { id: 32, ts: 32, level: 2, tag: 'DROP', msg: 'other unread' },
        { id: 33, ts: 33, level: 2, tag: 'KEEP', msg: 'second match' },
      ], 33, 'keep', 1),
    })

    expect(wrapper.get('.unread-button').text()).toContain('2 条新日志')
  })

  it('counts a matching arrival when the paused search and logs change in the same batch', async () => {
    const initialLogs = makeLogs(30)
    const wrapper = track(mountViewer({ logs: initialLogs, paused: true }))
    await nextTick()

    const updatedLogs = [
      ...initialLogs,
      { id: 31, ts: 31, level: 2, tag: 'OTHER', msg: 'hidden arrival' },
      { id: 32, ts: 32, level: 2, tag: 'MATCH', msg: 'needle arrival' },
    ]
    await wrapper.setProps({
      searchKeyword: 'needle',
      logs: updatedLogs,
      receivedCount: 32,
      searchCountState: currentSearchCountState(updatedLogs, 32, 'needle', 1),
    })

    expect(wrapper.get('.unread-button').text()).toContain('1 条新日志')
  })

  it('reveals existing unread matches when a manually scrolled viewer changes search', async () => {
    const initialLogs = makeLogs(30)
    const wrapper = track(mountViewer({ logs: initialLogs }))
    await nextTick()

    const scroller = wrapper.get('.log-scroller').element as HTMLElement
    scroller.scrollTop = 80
    await scroller.dispatchEvent(new Event('scroll'))
    const updatedLogs = [
      ...initialLogs,
      { id: 31, ts: 31, level: 2, tag: 'KEEP', msg: 'existing unread match' },
      { id: 32, ts: 32, level: 2, tag: 'DROP', msg: 'other unread' },
    ]
    await wrapper.setProps({
      logs: updatedLogs,
      receivedCount: 32,
    })

    await wrapper.setProps({
      searchKeyword: 'keep',
      searchCountState: currentSearchCountState(updatedLogs, 32, 'keep', 1),
    })

    expect(scroller.scrollTop).toBe(80)
    expect(wrapper.get('.unread-button').text()).toContain('1 条新日志')
  })

  it('keeps manual-scroll unread state on resume and clears it only after returning to bottom', async () => {
    const initialLogs = makeLogs(30)
    const wrapper = track(mountViewer({ logs: initialLogs, paused: true }))
    await nextTick()

    const scroller = wrapper.get('.log-scroller').element as HTMLElement
    scroller.scrollTop = 80
    await scroller.dispatchEvent(new Event('scroll'))
    await wrapper.setProps({
      logs: [...initialLogs, ...makeLogs(2, 30)],
      receivedCount: 32,
    })
    await wrapper.setProps({ paused: false })

    expect(scroller.scrollTop).toBe(80)
    expect(wrapper.get('.unread-button').text()).toContain('2 条新日志')

    await wrapper.get('.unread-button').trigger('click')
    expect(wrapper.find('.unread-button').exists()).toBe(false)
  })

  it('resets unread state on clear and starts counting from the cleared generation', async () => {
    const initialLogs = makeLogs(30)
    const wrapper = track(mountViewer({ logs: initialLogs, receivedCount: 30, paused: true }))
    await nextTick()

    await wrapper.setProps({ logs: [...initialLogs, ...makeLogs(4, 30)], receivedCount: 34 })
    expect(wrapper.get('.unread-button').text()).toContain('4 条新日志')

    await wrapper.setProps({ logs: [], receivedCount: 34, generation: 1 })
    expect(wrapper.find('.unread-button').exists()).toBe(false)

    await wrapper.setProps({ logs: makeLogs(2, 34), receivedCount: 36 })
    expect(wrapper.get('.unread-button').text()).toContain('2 条新日志')
  })

  it('treats clear and arrivals delivered in one update as the first batch of the new generation', async () => {
    const initialLogs = makeLogs(30)
    const wrapper = track(mountViewer({ logs: initialLogs, receivedCount: 30, paused: true }))
    await nextTick()

    await wrapper.setProps({ logs: [...initialLogs, ...makeLogs(4, 30)], receivedCount: 34 })
    expect(wrapper.get('.unread-button').text()).toContain('4 条新日志')

    await wrapper.setProps({ logs: makeLogs(3, 34), receivedCount: 37, generation: 1 })

    expect(wrapper.get('.unread-button').text()).toContain('3 条新日志')
  })

  it('treats searched clear and arrivals in one update as unread in the new generation', async () => {
    const initialLogs = makeLogs(30)
    const wrapper = track(mountViewer({
      logs: initialLogs,
      receivedCount: 30,
      paused: true,
      searchKeyword: 'match',
      searchCountState: { epoch: 1, baselineId: 30, baselineMatchIds: [], matchedAfterBaseline: 0 },
    }))
    await nextTick()

    await wrapper.setProps({
      logs: [
        { id: 31, ts: 31, level: 2, tag: 'MATCH', msg: 'same batch match' },
        { id: 32, ts: 32, level: 2, tag: 'OTHER', msg: 'same batch miss' },
      ],
      receivedCount: 32,
      generation: 1,
      searchCountState: { epoch: 1, baselineId: 30, baselineMatchIds: [], matchedAfterBaseline: 1 },
    })

    expect(wrapper.get('.unread-button').text()).toContain('1 条新日志')
  })

  it('exposes an accessible terminal region, log, and live unread status', async () => {
    const initialLogs = makeLogs(30)
    const wrapper = track(mountViewer({ logs: initialLogs, receivedCount: 30 }))
    await nextTick()

    expect(wrapper.get('[role="region"]').attributes('aria-label')).toBe('实时日志终端')
    expect(wrapper.get('[role="log"]').attributes('aria-label')).toBe('实时日志内容')

    const scroller = wrapper.get('.log-scroller').element as HTMLElement
    scroller.scrollTop = 80
    await scroller.dispatchEvent(new Event('scroll'))
    await wrapper.setProps({ logs: [...initialLogs, ...makeLogs(1, 30)], receivedCount: 31 })

    expect(wrapper.get('[aria-live="polite"]').text()).toContain('1 条新日志')
  })

  it('keeps virtual-scroller wrappers overflow-visible so long lines can scroll horizontally', async () => {
    const wrapper = track(mountViewer({
      logs: [{ id: 1, ts: 1, level: 2, tag: 'LONG', msg: 'x'.repeat(2000) }],
      receivedCount: 1,
    }))
    await nextTick()

    const scroller = wrapper.get('.log-scroller')
    const itemWrapper = wrapper.get('.vue-recycle-scroller__item-wrapper')
    const itemView = wrapper.get('.vue-recycle-scroller__item-view')

    expect(scroller.classes()).toContain('log-scroller')
    expect(itemWrapper.classes()).toContain('vue-recycle-scroller__item-wrapper')
    expect(itemView.classes()).toContain('vue-recycle-scroller__item-view')
    expect(wrapper.get('.log-line').classes()).toContain('log-line')
    expect(LogRealtimeViewerSource).toContain('overflow: auto;')
    expect(LogRealtimeViewerSource).toMatch(
      /\.log-scroller :deep\(\.vue-recycle-scroller__item-wrapper\)[\s\S]*?overflow: visible;/,
    )
    expect(LogRealtimeViewerSource).toMatch(
      /\.log-scroller :deep\(\.vue-recycle-scroller__item-view\)[\s\S]*?width: max-content;/,
    )
  })

  it('filters logs by tag, message, and level without mutating the source list', async () => {
    const logs = [
      { id: 1, ts: 1_000_000, level: 0, tag: 'NETWORK', msg: 'socket closed' },
      { id: 2, ts: 2_000_000, level: 2, tag: 'SENSOR', msg: 'temperature ready' },
      { id: 3, ts: 3_000_000, level: 3, tag: 'SYSTEM', msg: 'boot complete' },
    ]
    const wrapper = track(mountViewer({ logs, searchKeyword: 'temperature' }))
    await nextTick()

    expect(wrapper.get('.log-summary').text()).toContain('1 / 3')
    expect(wrapper.findAll('.log-line')).toHaveLength(1)
    expect(wrapper.get('.log-line').text()).toContain('temperature ready')

    await wrapper.setProps({ searchKeyword: 'error' })
    expect(wrapper.findAll('.log-line')).toHaveLength(1)
    expect(wrapper.get('.log-line').text()).toContain('socket closed')

    await wrapper.setProps({ searchKeyword: 'missing' })
    expect(wrapper.get('.log-empty').text()).toBe('无搜索命中')
    expect(wrapper.get('.export-text-button').attributes('disabled')).toBeDefined()
    expect(logs).toHaveLength(3)
  })

  it('keeps pause and manual scroll lock when the search changes', async () => {
    const initialLogs = makeLogs(30)
    const pausedWrapper = track(mountViewer({ logs: initialLogs, paused: true }))
    await nextTick()
    const pausedScroller = pausedWrapper.get('.log-scroller').element as HTMLElement
    pausedScroller.scrollTop = 80
    await pausedScroller.dispatchEvent(new Event('scroll'))

    await pausedWrapper.setProps({ searchKeyword: 'message' })

    expect(pausedWrapper.get('.pause-button').text()).toBe('继续')
    expect(pausedScroller.scrollTop).toBe(80)

    const scrollingWrapper = track(mountViewer({ logs: initialLogs }))
    await nextTick()
    const scrollingScroller = scrollingWrapper.get('.log-scroller').element as HTMLElement
    scrollingScroller.scrollTop = 80
    await scrollingScroller.dispatchEvent(new Event('scroll'))

    await scrollingWrapper.setProps({ searchKeyword: 'message' })

    expect(scrollingScroller.scrollTop).toBe(80)
  })

  it('only counts newly received logs that are visible in the active search', async () => {
    const initialLogs = makeLogs(30)
    const wrapper = track(mountViewer({
      logs: initialLogs,
      receivedCount: 30,
      paused: true,
      searchKeyword: 'match',
    }))
    await nextTick()

    const firstArrival = [
      ...initialLogs,
      { id: 31, ts: 31, level: 2, tag: 'OTHER', msg: 'not visible' },
    ]
    await wrapper.setProps({
      logs: firstArrival,
      receivedCount: 31,
      searchCountState: {
        epoch: 1,
        baselineId: 30,
        baselineMatchIds: [],
        matchedAfterBaseline: 0,
      },
    })
    expect(wrapper.find('.unread-button').exists()).toBe(false)

    await wrapper.setProps({
      logs: [
        ...firstArrival,
        { id: 32, ts: 32, level: 2, tag: 'MATCH', msg: 'visible' },
      ],
      receivedCount: 32,
      searchCountState: {
        epoch: 1,
        baselineId: 30,
        baselineMatchIds: [],
        matchedAfterBaseline: 1,
      },
    })
    expect(wrapper.get('.unread-button').text()).toContain('1 条新日志')
  })

  it('keeps exact searched unread arrivals after matching rows are trimmed', async () => {
    const initialLogs = makeLogs(30)
    const searchCountState: SearchCountState = {
      epoch: 1,
      baselineId: 30,
      baselineMatchIds: [],
      matchedAfterBaseline: 0,
    }
    const wrapper = track(mountViewer({
      logs: initialLogs,
      receivedCount: 30,
      paused: true,
      searchKeyword: 'match',
      searchCountState,
    }))
    await nextTick()

    const arrivals = Array.from({ length: 6000 }, (_, index): LogLine => ({
      id: 31 + index,
      ts: index,
      level: 2,
      tag: index % 2 === 0 ? 'MATCH' : 'OTHER',
      msg: `arrival ${index}`,
    }))
    await wrapper.setProps({
      logs: arrivals.slice(-5000),
      receivedCount: 6030,
      searchCountState: { ...searchCountState, matchedAfterBaseline: 3000 },
    })

    expect(wrapper.get('.unread-button').text()).toContain('3000 条新日志')
  })

  it('rebases a changed search to the bounded retained window and counts later arrivals', async () => {
    const retained = Array.from({ length: 5000 }, (_, index): LogLine => ({
      id: 1001 + index,
      ts: index,
      level: 2,
      tag: index % 4 === 0 ? 'SECOND' : 'OTHER',
      msg: `retained ${index}`,
    }))
    const wrapper = track(mountViewer({
      logs: [],
      receivedCount: 0,
      paused: true,
      searchKeyword: 'first',
      searchCountState: {
        epoch: 1,
        baselineId: 0,
        baselineMatchIds: [],
        matchedAfterBaseline: 0,
      },
    }))
    await nextTick()

    await wrapper.setProps({
      logs: retained,
      receivedCount: 6000,
      searchCountState: {
        epoch: 1,
        baselineId: 0,
        baselineMatchIds: [],
        matchedAfterBaseline: 1800,
      },
    })
    expect(wrapper.get('.unread-button').text()).toContain('1800 条新日志')

    const secondMatchIds = retained.filter(line => line.tag === 'SECOND').map(line => line.id)
    await wrapper.setProps({
      searchKeyword: 'second',
      searchCountState: {
        epoch: 2,
        baselineId: 6000,
        baselineMatchIds: secondMatchIds,
        matchedAfterBaseline: 0,
      },
    })

    expect(secondMatchIds).toHaveLength(1250)
    expect(wrapper.get('.unread-button').text()).toContain('1250 条新日志')

    await wrapper.setProps({
      logs: [
        ...retained.slice(0, -2),
        { id: 6001, ts: 1, level: 2, tag: 'SECOND', msg: 'new match' },
        { id: 6002, ts: 2, level: 2, tag: 'OTHER', msg: 'new miss' },
      ],
      receivedCount: 6002,
      searchCountState: {
        epoch: 2,
        baselineId: 6000,
        baselineMatchIds: secondMatchIds,
        matchedAfterBaseline: 1,
      },
    })

    expect(wrapper.get('.unread-button').text()).toContain('1251 条新日志')
  })

  it('clears searched unread after returning to bottom even when matches were trimmed', async () => {
    const wrapper = track(mountViewer({
      logs: [],
      receivedCount: 0,
      paused: true,
      searchKeyword: 'match',
      searchCountState: { epoch: 1, baselineId: 0, baselineMatchIds: [], matchedAfterBaseline: 0 },
    }))
    await nextTick()

    const retained = Array.from({ length: 5000 }, (_, index): LogLine => ({
      id: 1001 + index,
      ts: index,
      level: 2,
      tag: index % 2 === 0 ? 'MATCH' : 'OTHER',
      msg: `arrival ${index}`,
    }))
    await wrapper.setProps({
      logs: retained,
      receivedCount: 6000,
      searchCountState: { epoch: 1, baselineId: 0, baselineMatchIds: [], matchedAfterBaseline: 3000 },
    })
    expect(wrapper.get('.unread-button').text()).toContain('3000 条新日志')

    await wrapper.get('.unread-button').trigger('click')

    expect(wrapper.find('.unread-button').exists()).toBe(false)
  })

  it('renders toolbar actions as small Element Plus buttons with semantic state and icons', async () => {
    const wrapper = track(mountViewer({ logs: makeLogs(3) }))
    await nextTick()

    const pauseButton = wrapper.get('.pause-button')
    const clearButton = wrapper.get('.clear-button')
    const exportTextButton = wrapper.get('.export-text-button')
    const exportCsvButton = wrapper.get('.export-csv-button')
    const toolbarButtons = [pauseButton, clearButton, exportTextButton, exportCsvButton]

    toolbarButtons.forEach((button) => {
      expect(button.element.tagName).toBe('BUTTON')
      expect(button.classes()).toContain('el-button')
      expect(button.classes()).toContain('el-button--small')
    })
    expect(pauseButton.classes()).not.toContain('el-button--warning')
    expect(pauseButton.findComponent(VideoPause).exists()).toBe(true)
    expect(clearButton.classes()).not.toContain('el-button--danger')
    expect(clearButton.findComponent(Delete).exists()).toBe(true)
    expect(exportTextButton.findComponent(Download).exists()).toBe(true)
    expect(exportCsvButton.findComponent(Download).exists()).toBe(true)

    await wrapper.setProps({ paused: true })

    expect(pauseButton.classes()).toContain('el-button--warning')
    expect(pauseButton.findComponent(VideoPlay).exists()).toBe(true)
    expect(LogRealtimeViewerSource).toMatch(
      /\.log-toolbar-actions[\s\S]*?gap: 8px;[\s\S]*?margin-left: auto;/,
    )
    expect(LogRealtimeViewerSource).toContain('width: 280px;')
    expect(LogRealtimeViewerSource).toMatch(
      /\.log-action-group :deep\(\.el-button \+ \.el-button\)[\s\S]*?margin-left: 0;/,
    )
  })

  it('keeps the title, count, search, and actions in one toolbar and emits search updates', async () => {
    const wrapper = track(mountViewer({ logs: makeLogs(3), searchKeyword: 'message' }))
    await nextTick()

    const toolbar = wrapper.get('.log-toolbar')
    expect(toolbar.get('.log-title').text()).toBe('实时日志')
    expect(toolbar.get('.log-summary').text()).toBe('3 / 3')
    expect((toolbar.get('[aria-label="搜索实时日志"]').element as HTMLInputElement).value).toBe('message')
    expect(toolbar.findAll('.pause-button')).toHaveLength(1)
    expect(toolbar.findAll('.clear-button')).toHaveLength(1)
    expect(toolbar.findAll('.export-text-button')).toHaveLength(1)
    expect(toolbar.findAll('.export-csv-button')).toHaveLength(1)

    await toolbar.get('[aria-label="搜索实时日志"]').setValue('error')

    expect(wrapper.emitted('update:searchKeyword')).toEqual([['error']])
  })

  it('groups related actions so responsive wrapping never splits individual button pairs', async () => {
    const wrapper = track(mountViewer({ logs: makeLogs(3) }))
    await nextTick()

    const actions = wrapper.get('.log-toolbar-actions')
    const directChildren = Array.from(actions.element.children)

    expect(directChildren).toHaveLength(3)
    expect(directChildren[0]?.classList.contains('log-search')).toBe(true)
    expect(directChildren[1]?.classList.contains('log-action-group')).toBe(true)
    expect(directChildren[1]?.classList.contains('log-control-group')).toBe(true)
    expect(directChildren[2]?.classList.contains('log-action-group')).toBe(true)
    expect(directChildren[2]?.classList.contains('log-export-group')).toBe(true)
    expect(actions.get('.log-control-group').findAll('.el-button')).toHaveLength(2)
    expect(actions.get('.log-control-group').find('.pause-button').exists()).toBe(true)
    expect(actions.get('.log-control-group').find('.clear-button').exists()).toBe(true)
    expect(actions.get('.log-export-group').findAll('.el-button')).toHaveLength(2)
    expect(actions.get('.log-export-group').find('.export-text-button').exists()).toBe(true)
    expect(actions.get('.log-export-group').find('.export-csv-button').exists()).toBe(true)
  })

  it('uses container width to keep the desktop toolbar single-line and wrap by planned groups', () => {
    expect(LogRealtimeViewerSource).toMatch(
      /\.log-realtime-viewer\s*{[\s\S]*?container-type:\s*inline-size;/,
    )
    expect(LogRealtimeViewerSource).toMatch(
      /\.log-toolbar\s*{[\s\S]*?flex-wrap:\s*nowrap;/,
    )
    expect(LogRealtimeViewerSource).toMatch(
      /\.log-toolbar-actions\s*{[\s\S]*?flex:\s*0 0 auto;[\s\S]*?flex-wrap:\s*nowrap;/,
    )
    expect(LogRealtimeViewerSource).toMatch(/\.log-search\s*{[\s\S]*?width:\s*280px;/)
    expect(LogRealtimeViewerSource).toMatch(
      /\.log-action-group\s*{[\s\S]*?flex-wrap:\s*nowrap;[\s\S]*?white-space:\s*nowrap;/,
    )
    expect(LogRealtimeViewerSource).toMatch(
      /@container\s*\(max-width:\s*900px\)[\s\S]*?\.log-toolbar\s*{[\s\S]*?flex-wrap:\s*wrap;[\s\S]*?\.log-toolbar-actions\s*{[\s\S]*?width:\s*100%;/,
    )
    expect(LogRealtimeViewerSource).toMatch(
      /@container\s*\(max-width:\s*600px\)[\s\S]*?\.log-toolbar-actions\s*{[\s\S]*?flex-wrap:\s*wrap;[\s\S]*?\.log-search\s*{[\s\S]*?flex:\s*1 1 100%;[\s\S]*?width:\s*100%;/,
    )
  })

  it('emits clear and both export actions from the toolbar', async () => {
    const wrapper = track(mountViewer({ logs: makeLogs(3) }))
    await nextTick()

    await wrapper.get('.clear-button').trigger('click')
    await wrapper.get('.export-text-button').trigger('click')
    await wrapper.get('.export-csv-button').trigger('click')

    expect(wrapper.emitted('clear')).toEqual([[]])
    expect(wrapper.emitted('export')).toEqual([['text'], ['csv']])
  })
})
