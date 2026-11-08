import { describe, expect, it } from 'vitest'
import { nextTick } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import BackfillProgressCard from '../BackfillProgressCard.vue'
import type { BackfillProgress } from '@/types/system'

/**
 * 回填进度卡渲染（契约 §4.6）—— 需求点名的三种状态。
 *
 * 三条都是**产品承诺**，不是实现细节：
 *   ① running —— 进度、已回填帧数、ETA 都要能看到（用户据此判断「还要等多久」）；
 *   ② done    —— 整卡不渲染（一次性任务完成后不该常驻首屏，也不该显示「已完成」）；
 *   ③ failed  —— 必须显示错误，**且不能隐藏**（历史数据没迁完是用户必须知道的事实）。
 *
 * 这里用注入的 options 而不是 mock 模块：轮询是本组件唯一的副作用来源，
 * 注入 fetcher/schedule 就能把「时间」变成可控参数 —— 不依赖 fake timers，
 * 也不会因为真实 5s 定时器让用例挂住（vitest 会等到所有 timer 结束）。
 */

/** 造一份回填快照（默认：生产中段，3.4M 帧跑了 42%）。 */
function makeProgress(patch: Partial<BackfillProgress> = {}): BackfillProgress {
  return {
    state: 'running',
    percent: 42.6,
    current_partition: 'unified_data_202610',
    frames_done: 1_234_567,
    frames_total: 3_419_991,
    eta_seconds: 20 * 60,
    retention_permanent: false,
    errors: [],
    ...patch,
  }
}

/**
 * 挂载组件并让首轮取数**确实落地**。
 *
 * 为什么不能只 flushPromises 一次：`onMounted(start)` → `void tick()` →
 * `await refresh()` 至少两跳微任务，且 refresh 内部还有 try/catch 分支。
 * 这里循环 flush 到 fetch 被调用且 DOM 稳定为止 —— 关键是**对 null / done
 * 这类"本该不渲染"的输入也必须真的等到 fetch 完成**，否则用例会在
 * "还没取数"的状态下断言"没有卡片"，那是恒真断言（假绿）。
 *
 * schedule 注入成「记录回调但不执行」：本 spec 只关心**首帧渲染**，
 * 轮询节律另有 useBackfillProgress.spec.ts 专门覆盖。
 */
async function mountCard(progress: BackfillProgress | null) {
  const scheduled: Array<() => void> = []
  let fetchCalls = 0
  const wrapper = mount(BackfillProgressCard, {
    props: {
      options: {
        fetch: () => {
          fetchCalls += 1
          return Promise.resolve(progress)
        },
        pollMs: 5000,
        schedule: (fn) => {
          scheduled.push(fn)
          return 0 as unknown as ReturnType<typeof setTimeout>
        },
        cancel: () => {},
      },
    },
  })
  // 等到「fetch 真的被调用过」＋「DOM 更新完」两个条件都成立，断言才有意义。
  for (let i = 0; i < 20 && fetchCalls === 0; i += 1) {
    await flushPromises()
  }
  if (fetchCalls === 0) throw new Error('首轮 fetch 从未被调用 —— 组件没启动轮询')
  await flushPromises()
  await nextTick()
  // 再让可能存在的第二跳（nextTick 内的重渲染）落地
  await flushPromises()
  return { wrapper, scheduled, fetchCalls: () => fetchCalls }
}

describe('回填进度卡：running 态', () => {
  it('渲染进度、已回填帧数、ETA、当前分区（用户判断「还要等多久」的全部依据）', async () => {
    const { wrapper } = await mountCard(makeProgress())
    expect(wrapper.find('[data-test="backfill-card"]').exists()).toBe(true)

    expect(wrapper.find('[data-test="backfill-state"]').text()).toBe('回填中')
    expect(wrapper.find('[data-test="backfill-percent"]').text()).toContain('43%')
    // 千分位：3.4M 帧不分组根本读不出来
    expect(wrapper.find('[data-test="backfill-frames"]').text()).toContain('1,234,567')
    expect(wrapper.find('[data-test="backfill-frames"]').text()).toContain('3,419,991')
    expect(wrapper.find('[data-test="backfill-eta"]').text()).toContain('约 20 分钟')
    // 分区名原样显示：排障时要拿它去查 pg_class，译成「2026年10月」反而对不上
    expect(wrapper.find('[data-test="backfill-partition"]').text()).toContain('unified_data_202610')
    wrapper.unmount()
  })

  it('进度条取的是 percent（显示层夹取到 0..100 整数）', async () => {
    const { wrapper } = await mountCard(makeProgress({ percent: 42.6 }))
    // test-setup 的 ElProgress stub 把 percentage 落到内层宽度上
    const inner = wrapper.find('.backfill-bar .el-progress-bar__inner')
    expect(inner.exists()).toBe(true)
    expect(inner.attributes('style')).toContain('width: 43%')
    wrapper.unmount()
  })

  it('frames_total 未知时不写出 0 分母（"1,234,567 / 0" 像分母错了）', async () => {
    const { wrapper } = await mountCard(makeProgress({ frames_total: 0 }))
    const frames = wrapper.find('[data-test="backfill-frames"]').text()
    expect(frames).not.toContain('/ 0')
    expect(frames).toContain('总数统计中')
    wrapper.unmount()
  })

  it('ETA 未算出（0）时显示占位符，不显示「剩余 0 秒」', async () => {
    const { wrapper } = await mountCard(makeProgress({ eta_seconds: 0 }))
    expect(wrapper.find('[data-test="backfill-eta"]').text()).toContain('—')
    expect(wrapper.text()).not.toContain('0 秒')
    wrapper.unmount()
  })

  it('永久保留时明说「不会删除任何历史记录」（否则「回填」二字容易被读成「要删老数据」）', async () => {
    const { wrapper } = await mountCard(makeProgress({ retention_permanent: true }))
    const hint = wrapper.find('[data-test="backfill-retention"]')
    expect(hint.exists()).toBe(true)
    expect(hint.text()).toContain('不会删除')
    wrapper.unmount()
  })

  it('非永久保留时不出现那句提示（否则会被误读成"配置了删除"）', async () => {
    const { wrapper } = await mountCard(makeProgress({ retention_permanent: false }))
    expect(wrapper.find('[data-test="backfill-retention"]').exists()).toBe(false)
    wrapper.unmount()
  })
})

describe('回填进度卡：done / idle 态（隐藏）', () => {
  it.each(['done', 'idle'] as const)('%s 态整卡不渲染（一次性任务完成后不该常驻首屏）', async (state) => {
    const { wrapper, fetchCalls } = await mountCard(makeProgress({ state, percent: 100 }))
    // 分母守卫：必须先确认真取过数，否则「没卡片」可能只是还没渲染（假绿）
    expect(fetchCalls(), '首轮 fetch 没发生，本断言无意义').toBeGreaterThan(0)
    expect(wrapper.find('[data-test="backfill-card"]').exists()).toBe(false)
    // 连「已完成」都不显示 —— 卡片是「正在进行的工作」的容器，不是状态面板
    expect(wrapper.text()).toBe('')
    wrapper.unmount()
  })

  it('接口拿不到（null）时同样不渲染，页面其余部分不受影响', async () => {
    const { wrapper, fetchCalls } = await mountCard(null)
    expect(fetchCalls()).toBeGreaterThan(0)
    expect(wrapper.find('[data-test="backfill-card"]').exists()).toBe(false)
    wrapper.unmount()
  })
})

describe('回填进度卡：disabled 态（运维关阀 —— 生产会真实出现）', () => {
  it('必须渲染「已关闭」信息卡，不得空白、不得报错', async () => {
    const { wrapper } = await mountCard(makeProgress({ state: 'disabled', percent: 0 }))
    const card = wrapper.find('[data-test="backfill-card"]')
    expect(card.exists(), 'disabled 被隐藏/未渲染 ⇒ 用户永远看不到「回填已关闭」').toBe(true)

    // 状态标签与信息块都要在
    expect(wrapper.find('[data-test="backfill-state"]').text()).toBe('已关闭')
    const notice = wrapper.find('[data-test="backfill-disabled"]')
    expect(notice.exists()).toBe(true)
    expect(notice.text()).toContain('历史数据回填已关闭')
    // 必须解释后果：老数据不会迁移到帧表
    expect(notice.text()).toContain('不会迁移')
    // 且要说清「其它功能不受影响」，否则用户会以为整个系统坏了
    expect(notice.text()).toContain('不受影响')
    wrapper.unmount()
  })

  it('disabled 是**信息**态不是错误态：不出现错误样式，也不出现进度条/ETA', async () => {
    const { wrapper } = await mountCard(makeProgress({ state: 'disabled' }))
    expect(wrapper.find('[data-test="backfill-failed"]').exists()).toBe(false)
    expect(wrapper.find('.backfill-card').classes()).toContain('is-suppressed')
    expect(wrapper.find('.backfill-card').classes()).not.toContain('is-failed')

    // el-alert 的 type 必须落到 el-alert--info（test-setup 的替身把它写成 class）。
    // 用 error 会让「运维有意关阀」看起来像故障 —— 这是语义问题，不是配色偏好。
    const notice = wrapper.find('[data-test="backfill-disabled"]')
    expect(notice.classes(), 'disabled 用了错误态样式 ⇒ 把运维决策渲染成故障').toContain('el-alert--info')
    expect(notice.classes()).not.toContain('el-alert--error')
    // 没有在跑 ⇒ 没有进度可报，残留一条 0% 进度条会被读成「卡在 0%」
    expect(wrapper.find('[data-test="backfill-bar"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="backfill-eta"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('disabled 时不显示「回填在后台执行」这类现在时描述（会与事实矛盾）', async () => {
    const { wrapper } = await mountCard(makeProgress({ state: 'disabled' }))
    expect(wrapper.text()).not.toContain('回填在后台执行')
    wrapper.unmount()
  })
})

describe('回填进度卡：failed 态', () => {
  it('必须显示错误块且**不得隐藏**（历史数据没迁完，用户有权知道）', async () => {
    const { wrapper } = await mountCard(
      makeProgress({ state: 'failed', errors: ['partition unified_data_202609: encode frame 8123: value out of range'] }),
    )
    const card = wrapper.find('[data-test="backfill-card"]')
    expect(card.exists(), 'failed 时把整张卡隐藏了 ⇒ 用户会以为数据已迁完').toBe(true)

    const failed = wrapper.find('[data-test="backfill-failed"]')
    expect(failed.exists()).toBe(true)
    expect(failed.text()).toContain('历史数据回填中断')
    // 用户必须知道「已迁完的部分没事、重启会续跑」，否则会以为要重新跑一遍
    expect(failed.text()).toContain('断点继续')
    wrapper.unmount()
  })

  it('显示具体错误原因（取最近一条，后端最多保留 20 条）', async () => {
    const { wrapper } = await mountCard(
      makeProgress({ state: 'failed', errors: ['old error', 'latest error: encode failed'] }),
    )
    expect(wrapper.text()).toContain('latest error: encode failed')
    wrapper.unmount()
  })

  it('没有 errors 时不渲染空的原因行（空 <p> 会画出多余的行距）', async () => {
    const { wrapper } = await mountCard(makeProgress({ state: 'failed', errors: [] }))
    expect(wrapper.find('[data-test="backfill-failed"]').exists()).toBe(true)
    // 原因行用的是 .backfill-alert__detail，errors 为空时不应存在
    expect(wrapper.find('.backfill-alert__detail').exists()).toBe(false)
    wrapper.unmount()
  })

  it('错误文案不得泄露内部键名以外的东西：分区名与错误原文都保留（排障需要）', async () => {
    const { wrapper } = await mountCard(
      makeProgress({ state: 'failed', errors: ['partition unified_data_202609: boom'] }),
    )
    const text = wrapper.text()
    expect(text).toContain('unified_data_202609')
    expect(text).toContain('boom')
    wrapper.unmount()
  })
})
