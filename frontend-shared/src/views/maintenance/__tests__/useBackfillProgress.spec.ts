import { afterEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, onMounted } from 'vue'
import { mount } from '@vue/test-utils'
import { useBackfillProgress, MAX_CONSECUTIVE_NULLS } from '../useBackfillProgress'
import { BACKFILL_POLL_MS } from '../backfill'
import BackupCard from '../BackfillProgressCard.vue'
import type { BackfillProgress } from '@/types/system'

/**
 * 回填进度轮询（契约 §4.6）—— 需求点名的三条行为。
 *
 *   · 轮询间隔 5s；
 *   · state = done/idle 时**停止**轮询（不再向后端要数据）；
 *   · failed **不**停（容器重启后会从断点续跑，停了就再也恢复不了）。
 *
 * 为什么用「注入排程器」而不是 vi.useFakeTimers：
 * 本组合式函数的全部时间行为都集中在 schedule/cancel 两个入口，
 * 注入一个手动缸（手动决定何时触发下一 tick）比伪造全局时钟更精确 ——
 * 而且能断言「有没有排下一次」这个事实本身（fake timers 下要数 pending 数，
 * 那依赖 vitest 内部实现，脆弱）。
 */

/** 手动排程缸：记录所有被排的回调，由用例决定何时执行。 */
function makeClock() {
  const pending: Array<{ fn: () => void; ms: number }> = []
  return {
    pending,
    schedule: (fn: () => void, ms: number) => {
      pending.push({ fn, ms })
      return (pending.length - 1) as unknown as ReturnType<typeof setTimeout>
    },
    cancel: (handle: ReturnType<typeof setTimeout>) => {
      const index = handle as unknown as number
      if (pending[index]) pending[index].fn = () => {}
    },
    /** 执行并取出最近一次排程（模拟时间到达）。 */
    async runNext() {
      const next = pending.shift()
      if (!next) throw new Error('没有待执行的 tick —— 轮询提前停了')
      next.fn()
      // tick 内部是 async（fetch → 排下一次），flush 两跳
      await Promise.resolve()
      await Promise.resolve()
      await Promise.resolve()
    },
    get size() {
      return pending.filter((p) => p.fn.toString() !== '() => {}').length
    },
  }
}

function makeProgress(patch: Partial<BackfillProgress> = {}): BackfillProgress {
  return {
    state: 'running',
    percent: 10,
    current_partition: 'unified_data_202610',
    frames_done: 100,
    frames_total: 1000,
    eta_seconds: 60,
    retention_permanent: false,
    errors: [],
    ...patch,
  }
}

describe('轮询间隔与启动', () => {
  it('默认 5s；start 会立刻取一次数（不等到第一个 5s）', async () => {
    const clock = makeClock()
    const fetch = vi.fn(() => Promise.resolve(makeProgress()))
    const state = useBackfillProgress({ fetch, schedule: clock.schedule, cancel: clock.cancel })

    expect(state.progress.value).toBeNull()
    state.start()
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()

    expect(fetch, 'start 后必须立刻取一次，否则首屏 5s 空白').toHaveBeenCalledTimes(1)
    expect(state.progress.value?.state).toBe('running')
    expect(clock.pending[0]?.ms, '下一次必须在 5s 后').toBe(5000)
  })

  it('start 幂等：连调两次不会并发两条轮询链', async () => {
    const clock = makeClock()
    const fetch = vi.fn(() => Promise.resolve(makeProgress()))
    const state = useBackfillProgress({ fetch, schedule: clock.schedule, cancel: clock.cancel })

    state.start()
    state.start()
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()

    expect(fetch).toHaveBeenCalledTimes(1)
    expect(clock.pending).toHaveLength(1)
  })
})

describe('done / idle 时停止轮询', () => {
  it.each(['done', 'idle', 'disabled'] as const)('state=%s：拿到终态后不再排下一次', async (terminal) => {
    const clock = makeClock()
    const fetch = vi.fn(() => Promise.resolve(makeProgress({ state: terminal })))
    const state = useBackfillProgress({ fetch, schedule: clock.schedule, cancel: clock.cancel })

    state.start()
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()

    expect(state.progress.value?.state).toBe(terminal)
    expect(clock.pending, '终态后仍排了下一次 ⇒ 会永远问一个不会再变的值').toHaveLength(0)
    expect(state.polling.value).toBe(false)
  })

  it('running → done 的转换发生在轮询途中：这一轮结束后就停', async () => {
    const clock = makeClock()
    const responses = [makeProgress({ state: 'running', percent: 90 }), makeProgress({ state: 'done', percent: 100 })]
    const fetch = vi.fn(() => Promise.resolve(responses.shift() ?? null))
    const state = useBackfillProgress({ fetch, schedule: clock.schedule, cancel: clock.cancel })

    state.start()
    await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); await Promise.resolve()
    expect(clock.pending).toHaveLength(1)

    await clock.runNext()
    expect(fetch).toHaveBeenCalledTimes(2)
    expect(state.progress.value?.state).toBe('done')
    expect(clock.pending, 'done 之后还排了 tick').toHaveLength(0)
  })
})

describe('failed 不停止轮询', () => {
  it('state=failed 仍继续排下一次（容器重启续跑后要能自动恢复）', async () => {
    const clock = makeClock()
    const fetch = vi.fn(() => Promise.resolve(makeProgress({ state: 'failed', errors: ['boom'] })))
    const state = useBackfillProgress({ fetch, schedule: clock.schedule, cancel: clock.cancel })

    state.start()
    await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); await Promise.resolve()

    expect(state.progress.value?.state).toBe('failed')
    expect(
      clock.pending,
      'failed 后停止轮询 ⇒ 后端重启续跑时卡片永远停在「已中断」，用户只能手刷',
    ).toHaveLength(1)
    expect(state.polling.value).toBe(true)
  })

  it('failed → running 的自动恢复（后端重启续跑）', async () => {
    const clock = makeClock()
    const responses = [makeProgress({ state: 'failed' }), makeProgress({ state: 'running', percent: 20 })]
    const fetch = vi.fn(() => Promise.resolve(responses.shift() ?? null))
    const state = useBackfillProgress({ fetch, schedule: clock.schedule, cancel: clock.cancel })

    state.start()
    await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); await Promise.resolve()
    expect(state.progress.value?.state).toBe('failed')

    await clock.runNext()
    expect(state.progress.value?.state).toBe('running')
  })
})

describe('取数失败（null）的容忍', () => {
  it('单次 null 不停止轮询（引擎切换窗口是毫秒级，停掉就再也起不来）', async () => {
    const clock = makeClock()
    let calls = 0
    const fetch = vi.fn(() => {
      calls += 1
      return Promise.resolve(calls === 1 ? null : makeProgress())
    })
    const state = useBackfillProgress({ fetch, schedule: clock.schedule, cancel: clock.cancel })

    state.start()
    await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); await Promise.resolve()
    expect(state.progress.value).toBeNull()
    expect(clock.pending, '第一次拿不到就停止轮询了').toHaveLength(1)
  })

  it('连续失败到上限后停止（避免对一个不存在的接口无限请求）', async () => {
    const clock = makeClock()
    const fetch = vi.fn(() => Promise.resolve(null))
    const state = useBackfillProgress({ fetch, schedule: clock.schedule, cancel: clock.cancel })

    state.start()
    await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); await Promise.resolve()

    for (let i = 1; i < MAX_CONSECUTIVE_NULLS; i += 1) {
      await clock.runNext()
    }

    expect(state.nullStreak.value).toBe(MAX_CONSECUTIVE_NULLS)
    expect(clock.pending, '到上限后仍继续请求').toHaveLength(0)
    expect(state.polling.value).toBe(false)
  })

  it('上限是 24 次 × 5s = 2 分钟（覆盖引擎切换窗口，又不会永久空转）', () => {
    expect(MAX_CONSECUTIVE_NULLS).toBe(24)
    expect(MAX_CONSECUTIVE_NULLS * 5000).toBe(120_000)
  })

  it('fetcher 抛异常也按「拿不到」处理，不让一次异常把页面带崩', async () => {
    const clock = makeClock()
    const fetch = vi.fn(() => Promise.reject(new Error('network down')))
    const state = useBackfillProgress({ fetch, schedule: clock.schedule, cancel: clock.cancel })

    state.start()
    await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); await Promise.resolve()

    expect(state.progress.value).toBeNull()
    expect(state.nullStreak.value).toBe(1)
    // 抛异常不等于终态，仍要继续试
    expect(clock.pending).toHaveLength(1)
  })
})

describe('stop 与卸载', () => {
  it('stop 后不再排下一次（页面切走不该继续烧请求）', async () => {
    const clock = makeClock()
    const fetch = vi.fn(() => Promise.resolve(makeProgress()))
    const state = useBackfillProgress({ fetch, schedule: clock.schedule, cancel: clock.cancel })

    state.start()
    await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); await Promise.resolve()
    expect(clock.pending).toHaveLength(1)

    state.stop()
    expect(state.polling.value).toBe(false)
    // stop 取消了待执行的回调（makeClock.cancel 把 fn 置空）
    expect(clock.size).toBe(0)
  })

  it('stop 之后 start 不会复活（组件已卸载，复活就是定时器泄漏）', async () => {
    const clock = makeClock()
    const fetch = vi.fn(() => Promise.resolve(makeProgress()))
    const state = useBackfillProgress({ fetch, schedule: clock.schedule, cancel: clock.cancel })

    state.stop()
    state.start()
    await Promise.resolve(); await Promise.resolve(); await Promise.resolve()

    expect(fetch, 'stop 之后 start 复活了轮询').not.toHaveBeenCalled()
  })
})

/* ════════════════════════════════════════════════════════════════════════
 * 回归门禁：定时器不得泄漏（本轮实测踩过的坑）
 * ════════════════════════════════════════════════════════════════════════
 *
 * 缺陷现场（2026-11-07）：Monitor.spec 的 @/api/system mock 默认返回 null，
 * 而 null 不是终态 ⇒ 每条用例挂载都排一条**真实** 5s setTimeout 链；
 * 该 spec 里大量用例并不 unmount ⇒ 定时器全留在 worker 里。
 * 后果：全套 vitest 从 **94s 涨到 699s**，进而把**别人的**用例
 * （sessionValidityGuard / FixedColumnOcclusion）的超时预算挤爆，报成
 * 「那两个文件超时」—— 受害者不是本次改动，诊断方向被彻底带偏。
 *
 * 因此本门禁守的不是"某个 mock 的默认值"，而是根因：
 * **未到终态时，组件销毁必须把挂起的 setTimeout 清掉**。
 * 它防的是「以后任何人再加轮询组件时重演同一个 699s」。
 *
 * 为什么用 vi.useFakeTimers() + vi.getTimerCount() 而不是暴露内部 onCleanup 计数：
 * 计数只有在"忘了调 stop"时才为 0，而真实故障形态是「stop 调了但没 clearTimeout」
 * 或「clearTimeout 清错了句柄」—— 只有数**真实挂起的定时器**才能抓到这两种。
 * 这是"测行为而不是测实现"的同一原则：断言的是 vitest 眼里的定时器，不是我自己
 * 声明的一个数字。
 */
describe('回归门禁：卸载必须清除挂起的轮询定时器', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  /**
   * 最小宿主组件：调用组合式函数并**照真实用法启动轮询**。
   *
   * ⚠️ 必须显式 onMounted(start)：useBackfillProgress **不会**自动开始轮询 ——
   * 启动时机交给调用方（真实消费者 BackfillProgressCard 在 onMounted 里 start）。
   * 曾漏写这句，导致 fetch 一次都没被调、"卸载后为 0" 变成恒真断言（假绿）。
   */
  function makeHost(fetch: () => Promise<BackfillProgress | null>) {
    return defineComponent({
      setup() {
        const { start } = useBackfillProgress({ fetch })
        onMounted(start)
        return () => h('div', { class: 'host' })
      },
    })
  }

  /** 冲掉「fetch 的微任务 → 排下一次定时器」这条链（fake timers 不影响微任务）。 */
  const settle = async () => {
    for (let i = 0; i < 4; i += 1) await Promise.resolve()
  }

  /*
   * ⚠️ 基数说明（实测得出，不是想当然）：
   * test-utils 的 mount() **本身会留下 1 个定时器**，且 unmount 不会清它
   * （实测：不调用任何组合式函数，裸 mount/unmount 也是 +1 且保持）。
   * 因此判据一律写成「相对**挂载后同步时点**的差值」，而不是绝对归零 ——
   * 绝对归零会把 VTU 自己的定时器算成我们的泄漏（假红），
   * 或者让我们的泄漏被 VTU 的余量掩盖（假绿）。
   *
   * 时序：mount（同步）→ VTU 定时器已在，我们的轮询**还没排**（要等 fetch 微任务）
   *      → settle() → 我们的轮询 +1
   *      → unmount → 必须回到 mount 后同步时点的值
   */

  it('未到终态（null）时确实多出一个轮询定时器 —— 先证明"有东西可泄漏"', async () => {
    vi.useFakeTimers()
    const fetch = vi.fn(() => Promise.resolve(null))
    const wrapper = mount(makeHost(fetch))

    const afterMount = vi.getTimerCount()
    await settle()

    expect(fetch).toHaveBeenCalledTimes(1)
    expect(
      vi.getTimerCount() - afterMount,
      '非终态下没排轮询定时器 ⇒ 本门禁失去意义（下面"卸载后归零"会恒真）',
    ).toBe(1)

    wrapper.unmount()
    expect(
      vi.getTimerCount() - afterMount,
      '卸载后仍残留轮询定时器 ⇒ 会像 2026-11-07 那样把所有用例的超时预算慢慢耗光',
    ).toBe(0)
  })

  it.each([
    [
      'null（接口不可用 / 未登录）',
      null,
    ],
    [
      'failed（断点保留，会继续轮询）',
      {
        state: 'failed',
        percent: 10,
        current_partition: '',
        frames_done: 1,
        frames_total: 0,
        eta_seconds: 0,
        retention_permanent: false,
        errors: ['boom'],
      },
    ],
  ])('%s 时卸载组件 → 挂起的 setTimeout 必须清除', async (_label, payload) => {
    vi.useFakeTimers()
    const fetch = vi.fn(() => Promise.resolve(payload as unknown as BackfillProgress | null))
    const wrapper = mount(makeHost(fetch))
    const afterMount = vi.getTimerCount()

    await settle()
    expect(vi.getTimerCount() - afterMount, '前置条件不成立：没排上轮询定时器').toBe(1)

    wrapper.unmount()
    expect(
      vi.getTimerCount() - afterMount,
      '卸载后仍有挂起的轮询定时器 ⇒ 会像 2026-11-07 那样把全套耗时从 94s 拖到 699s',
    ).toBe(0)
  })

  it.each(['done', 'idle', 'disabled'] as const)(
    '终态 %s 本来就不排程 —— 卸载前后都不新增定时器（不制造假泄漏）',
    async (state) => {
      vi.useFakeTimers()
      const fetch = vi.fn(() =>
        Promise.resolve({
          state,
          percent: 100,
          current_partition: '',
          frames_done: 0,
          frames_total: 0,
          eta_seconds: 0,
          retention_permanent: false,
          errors: [],
        } as BackfillProgress),
      )
      const wrapper = mount(makeHost(fetch))
      const afterMount = vi.getTimerCount()

      await settle()
      expect(vi.getTimerCount() - afterMount, state + ' 是终态，却还排了轮询').toBe(0)

      wrapper.unmount()
      expect(vi.getTimerCount() - afterMount).toBe(0)
    },
  )

  it('真实消费者 BackfillProgressCard：running 时挂载会排程，卸载后清除', async () => {
    vi.useFakeTimers()
    const fetch = vi.fn(() =>
      Promise.resolve({
        state: 'running',
        percent: 42,
        current_partition: 'unified_data_202610',
        frames_done: 100,
        frames_total: 1000,
        eta_seconds: 60,
        retention_permanent: false,
        errors: [],
      } as BackfillProgress),
    )
    const wrapper = mount(BackupCard, { props: { options: { fetch } } })
    const afterMount = vi.getTimerCount()

    await settle()
    expect(wrapper.find('[data-test="backfill-card"]').exists()).toBe(true)
    expect(vi.getTimerCount() - afterMount, '真实组件挂载后应有一条轮询').toBe(1)

    wrapper.unmount()
    expect(
      vi.getTimerCount() - afterMount,
      '真实卡片卸载后泄漏了定时器 —— Monitor 页来回切换会不断累积',
    ).toBe(0)
  })

  it('排程用的是 BACKFILL_POLL_MS（5s），不是魔法数字；且到点后继续排队', async () => {
    vi.useFakeTimers()
    expect(BACKFILL_POLL_MS).toBe(5000)
    const fetch = vi.fn(() => Promise.resolve(null))
    const wrapper = mount(makeHost(fetch))
    const afterMount = vi.getTimerCount()
    await settle()

    // 4.9s 时不该再次取数；5s 才触发
    vi.advanceTimersByTime(BACKFILL_POLL_MS - 100)
    expect(fetch).toHaveBeenCalledTimes(1)
    vi.advanceTimersByTime(200)
    expect(fetch).toHaveBeenCalledTimes(2)

    wrapper.unmount()
    expect(vi.getTimerCount() - afterMount).toBe(0)
  })

  it('多轮挂载/卸载不累积定时器（Monitor 页来回切换的真实场景）', async () => {
    vi.useFakeTimers()
    // 先取一次基数：第一次 mount 会引入 VTU 自己那个定时器
    const warm = mount(makeHost(() => Promise.resolve(null)))
    const base = vi.getTimerCount()
    warm.unmount()

    for (let i = 0; i < 5; i += 1) {
      const w = mount(makeHost(() => Promise.resolve(null)))
      await settle()
      expect(vi.getTimerCount() - base, '第 ' + (i + 1) + ' 轮挂载后定时器数不对').toBe(1)
      w.unmount()
      expect(vi.getTimerCount() - base, '第 ' + (i + 1) + ' 轮卸载后残留').toBe(0)
    }
  })
})
