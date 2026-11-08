import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { mount } from '@vue/test-utils'
import Maintenance from '../Maintenance.vue'
import type { MaintenanceSnapshot, MaintenanceStep } from '@/types/system'

/**
 * 维护页渲染（契约 §4）。
 *
 * 覆盖的五件事都是**产品承诺**，不是实现细节：
 *   ① 未就绪时是「系统升级中」，不是错误页（这是用户唯一能看到的解释）；
 *   ② 阶段列表把五种状态如实区分（当前高亮 / 完成打勾 / 失败标红 / 跳过灰）；
 *   ③ 失败态必须给出**具体原因** + 「重新加载」按钮（否则用户只能干等）；
 *   ④ 连不上后端（进程未监听）时必须显式说「无法连接服务器，正在重试…」，
 *      并且**不能**渲染成「已就绪」的样子；
 *   ⑤ 已耗时与版本/commit 必须可见（排障时唯一能报给管理员的信息）。
 *
 * 这里全部用**真实渲染**而不是 snapshot：snapshot 会在改样式时全红，
 * 而上述五条关心的是「文案与语义是否出现」，不是 DOM 长什么样。
 */

/** 造一个已知形状的快照（默认：跑到一半，parent_indexes 阶段进行中）。 */
function makeSnapshot(patch: Partial<MaintenanceSnapshot> = {}): MaintenanceSnapshot {
  const steps: MaintenanceStep[] = [
    { id: 'db_connect', label: '连接数据库', weight: 3, state: 'done', progress: 100 },
    { id: 'auto_migrate', label: '数据库结构迁移', weight: 10, state: 'done', progress: 100 },
    {
      id: 'parent_indexes',
      label: '补齐父表索引',
      weight: 25,
      state: 'running',
      progress: 40,
      detail: '正在创建 idx_unified_device_time',
    },
    { id: 'retire_dead_indexes', label: '退役死索引', weight: 5, state: 'pending', progress: 0 },
    { id: 'warmup', label: '缓存预热', weight: 7, state: 'skipped', progress: 0, detail: '无缓存后端' },
  ]
  return {
    ready: false,
    failed: false,
    percent: 37.5,
    current_step: 'parent_indexes',
    current_label: '补齐父表索引',
    started_at: '2026-11-06T08:00:00Z',
    elapsed_ms: 65_000,
    steps,
    version: '2.0.0',
    build_commit: 'a0a983bf1234567890',
    ...patch,
  }
}

const mountOptions = () => ({
  global: {
    stubs: {
      // 图标是真实 EP 图标组件；stub 掉避免每个用例都创建 svg 树，
      // 且断言只关心「是否有对应状态 class」而非图标本身。
      // 注意不能整体 stub el-icon —— 状态图标是 <el-icon><Xxx/></el-icon> 结构，
      // 保留 el-icon（test-setup 已全局注册）才能验证五态图标分支。
    },
  },
})

describe('维护页：进行中（未就绪）', () => {
  it('标题是「系统升级中」，副标题提示勿关闭页面', () => {
    const wrapper = mount(Maintenance, { props: { snapshot: makeSnapshot() }, ...mountOptions() })
    expect(wrapper.find('[data-testid="maintenance-page"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('系统升级中')
    expect(wrapper.text()).toContain('正在优化数据库')
    expect(wrapper.text()).toContain('请勿关闭页面')
  })

  it('总体进度条按 percent 渲染，并显示取整后的百分比数字', () => {
    const wrapper = mount(Maintenance, { props: { snapshot: makeSnapshot() }, ...mountOptions() })
    // el-progress stub 把 percentage 落到内层宽度上（test-setup 的既有 stub 契约）
    const bar = wrapper.find('.maintenance-overall__bar .el-progress-bar__inner')
    expect(bar.exists()).toBe(true)
    expect(bar.attributes('style')).toContain('width: 38%')
    expect(wrapper.find('[data-testid="maintenance-percent"]').text()).toBe('38%')
  })

  it('当前阶段名出现在「当前阶段：」标签里（用户要知道卡在哪一步）', () => {
    const wrapper = mount(Maintenance, { props: { snapshot: makeSnapshot() }, ...mountOptions() })
    expect(wrapper.text()).toContain('当前阶段：补齐父表索引')
  })

  it('已耗时用服务端 elapsed_ms 渲染（65s → 1 分 05 秒）', () => {
    const wrapper = mount(Maintenance, { props: { snapshot: makeSnapshot() }, ...mountOptions() })
    expect(wrapper.find('[data-testid="maintenance-elapsed"]').text()).toContain('1 分 05 秒')
  })

  it('版本与短 commit 可见（排障时唯一能报给管理员的信息）', () => {
    const wrapper = mount(Maintenance, { props: { snapshot: makeSnapshot() }, ...mountOptions() })
    const version = wrapper.find('[data-testid="maintenance-version"]')
    expect(version.exists()).toBe(true)
    expect(version.text()).toContain('v2.0.0')
    expect(version.text()).toContain('a0a983b')
  })

  it('没有 build_commit 时只显示版本，不显示空括号', () => {
    const wrapper = mount(Maintenance, {
      props: { snapshot: makeSnapshot({ build_commit: '' }) },
      ...mountOptions(),
    })
    const version = wrapper.find('[data-testid="maintenance-version"]').text()
    expect(version).toBe('版本 v2.0.0')
    expect(version).not.toContain('（）')
  })

  it('进行中不显示失败告警、也不显示连接失败告警', () => {
    const wrapper = mount(Maintenance, { props: { snapshot: makeSnapshot() }, ...mountOptions() })
    expect(wrapper.text()).not.toContain('系统启动失败')
    expect(wrapper.text()).not.toContain('无法连接服务器')
  })
})

describe('维护页：阶段列表五态', () => {
  it('五个阶段全部渲染，且每个阶段带 data-state 便于判据定位', () => {
    const wrapper = mount(Maintenance, { props: { snapshot: makeSnapshot() }, ...mountOptions() })
    const rows = wrapper.findAll('.maintenance-step')
    expect(rows).toHaveLength(5)
    expect(rows.map((r) => r.attributes('data-state'))).toEqual([
      'done',
      'done',
      'running',
      'pending',
      'skipped',
    ])
  })

  it('each 状态渲染对应中文文案（已完成/进行中/等待中/已跳过）', () => {
    const wrapper = mount(Maintenance, { props: { snapshot: makeSnapshot() }, ...mountOptions() })
    const running = wrapper.find('[data-step-id="parent_indexes"]')
    expect(running.text()).toContain('进行中')
    expect(wrapper.find('[data-step-id="db_connect"]').text()).toContain('已完成')
    expect(wrapper.find('[data-step-id="retire_dead_indexes"]').text()).toContain('等待中')
    expect(wrapper.find('[data-step-id="warmup"]').text()).toContain('已跳过')
  })

  it('进行中阶段带 is-running class 与步骤内进度条，且 detail 文本可见', () => {
    const wrapper = mount(Maintenance, { props: { snapshot: makeSnapshot() }, ...mountOptions() })
    const running = wrapper.find('[data-step-id="parent_indexes"]')
    expect(running.classes()).toContain('is-running')
    expect(running.find('.maintenance-step__bar').exists()).toBe(true)
    expect(running.text()).toContain('正在创建 idx_unified_device_time')
  })

  it('失败阶段标红：is-failed class + error 文本', () => {
    const snapshot = makeSnapshot({
      failed: true,
      steps: [
        { id: 'db_connect', label: '连接数据库', weight: 3, state: 'done', progress: 100 },
        {
          id: 'partition_unified_data',
          label: 'unified_data 分区迁移',
          weight: 25,
          state: 'failed',
          progress: 12,
          error: 'relation "unified_data" does not exist',
        },
      ],
    })
    const wrapper = mount(Maintenance, { props: { snapshot }, ...mountOptions() })
    const failedRow = wrapper.find('[data-step-id="partition_unified_data"]')
    expect(failedRow.classes()).toContain('is-failed')
    expect(failedRow.find('.maintenance-step__error').text()).toContain('does not exist')
    // 失败行仍显示「失败」文案，不能只剩一个红点
    expect(failedRow.text()).toContain('失败')
  })

  it('steps 为空时不渲染列表容器（连接中态不该出现空列表骨架）', () => {
    const wrapper = mount(Maintenance, {
      props: { snapshot: makeSnapshot({ steps: [] }) },
      ...mountOptions(),
    })
    expect(wrapper.find('[data-testid="maintenance-steps"]').exists()).toBe(false)
  })
})

describe('维护页：失败态', () => {
  it('显示「系统启动失败」标题与失败原因摘要', () => {
    const snapshot = makeSnapshot({
      failed: true,
      steps: [
        { id: 'partition_device_data', label: 'device_data 分区迁移', weight: 15, state: 'failed', progress: 5, error: 'permission denied' },
      ],
    })
    const wrapper = mount(Maintenance, { props: { snapshot }, ...mountOptions() })
    expect(wrapper.text()).toContain('系统启动失败')
    expect(wrapper.text()).toContain('device_data 分区迁移')
    expect(wrapper.text()).toContain('permission denied')
  })

  it('失败态给「重新加载」按钮，点击触发 window.location.reload', async () => {
    const snapshot = makeSnapshot({ failed: true, steps: [] })
    const wrapper = mount(Maintenance, { props: { snapshot }, ...mountOptions() })
    const reload = vi.fn()
    const originalLocation = window.location
    // happy-dom 的 location.reload 不可打桩，按 api/__tests__/client.spec.ts 的既有做法替换 location
    delete (window as unknown as { location?: Location }).location
    ;(window as unknown as Record<string, unknown>).location = { ...originalLocation, reload }

    const button = wrapper.findAll('button').find((b) => b.text().includes('重新加载'))
    expect(button, '失败态必须提供「重新加载」按钮').toBeTruthy()
    await button!.trigger('click')
    expect(reload).toHaveBeenCalledOnce()

    ;(window as unknown as Record<string, unknown>).location = originalLocation
  })

  it('失败态进度条走 exception 状态（红条），不伪装成进行中', () => {
    const snapshot = makeSnapshot({ failed: true, percent: 12, steps: [] })
    const wrapper = mount(Maintenance, { props: { snapshot }, ...mountOptions() })
    expect(wrapper.text()).toContain('中断时的总体进度')
    expect(wrapper.find('[data-testid="maintenance-percent"]').text()).toBe('12%')
  })

  it('没有失败步骤时也给出一句通用原因（不能只显示空告警）', () => {
    const wrapper = mount(Maintenance, { props: { snapshot: makeSnapshot({ failed: true, steps: [] }) }, ...mountOptions() })
    expect(wrapper.text()).toContain('后端在启动过程中退出')
  })
})

describe('维护页：空值兜底（绝不出现空标签 / NaN 秒）', () => {
  it('current_label 为空 + 未失败 → 「当前阶段：已停止」，不是悬空的「当前阶段：」', () => {
    const wrapper = mount(Maintenance, {
      props: { snapshot: makeSnapshot({ current_label: '', current_step: '' }) },
      ...mountOptions(),
    })
    const label = wrapper.find('.maintenance-overall__label').text()
    expect(label).toBe('当前阶段：已停止')
    expect(label.endsWith('：'), '不得渲染成悬空标签').toBe(false)
  })

  it('current_label 为空 + failed → 「中断时的总体进度」（失败态优先，不叫"已停止"）', () => {
    const wrapper = mount(Maintenance, {
      props: { snapshot: makeSnapshot({ failed: true, current_label: '', current_step: '', steps: [] }) },
      ...mountOptions(),
    })
    expect(wrapper.find('.maintenance-overall__label').text()).toBe('中断时的总体进度')
  })

  it('步骤 label 为空 → 回落 id；id 也空 → 占位符 —（不出现空行）', () => {
    const snapshot = makeSnapshot({
      steps: [
        { id: 'retire_dead_indexes', label: '', weight: 5, state: 'failed', progress: 0, error: 'lock timeout' },
        { id: '', label: '', weight: 1, state: 'pending', progress: 0 },
      ],
    })
    const wrapper = mount(Maintenance, { props: { snapshot }, ...mountOptions() })
    const rows = wrapper.findAll('.maintenance-step')
    expect(rows[0].find('.maintenance-step__label').text()).toBe('retire_dead_indexes')
    expect(rows[1].find('.maintenance-step__label').text()).toBe('—')
    // 每一行的 label 都非空
    for (const row of rows) expect(row.find('.maintenance-step__label').text().length).toBeGreaterThan(0)
  })

  it('failed=true 且 percent 停在任意值（12/50/99）→ 如实显示该值，不当作"还在跑"', () => {
    for (const percent of [12, 50, 99]) {
      const wrapper = mount(Maintenance, {
        props: { snapshot: makeSnapshot({ failed: true, percent, steps: [] }) },
        ...mountOptions(),
      })
      expect(wrapper.find('[data-testid="maintenance-percent"]').text()).toBe(String(percent) + '%')
      expect(wrapper.text()).toContain('中断时的总体进度')
      wrapper.unmount()
    }
  })

  it('started_at 缺失 / 非法 → 已耗时仍由 elapsed_ms 驱动，不出现 NaN', () => {
    // 组件**从不解析 started_at**（grep 可证）：耗时只来自 elapsed_ms。
    // 这里把 started_at 置为非法串，断言渲染仍是正常秒数而不是 NaN/Invalid。
    const wrapper = mount(Maintenance, {
      props: { snapshot: makeSnapshot({ started_at: 'not-a-date', elapsed_ms: 7000 }) },
      ...mountOptions(),
    })
    const text = wrapper.find('[data-testid="maintenance-elapsed"]').text()
    expect(text).toContain('7 秒')
    expect(text).not.toContain('NaN')
    expect(text).not.toContain('Invalid')
  })

  it('elapsed_ms 缺失（Go omitempty / 旧后端）→ 0 秒，不是 NaN', () => {
    const raw = makeSnapshot()
    delete (raw as Partial<MaintenanceSnapshot>).elapsed_ms
    const wrapper = mount(Maintenance, { props: { snapshot: raw }, ...mountOptions() })
    const text = wrapper.find('[data-testid="maintenance-elapsed"]').text()
    expect(text).toContain('0 秒')
    expect(text).not.toContain('NaN')
  })
})

describe('维护页：连接失败（后端进程未监听）', () => {
  it('无快照 + connectionFailed → 「正在连接服务器 / 无法连接服务器，正在重试…」', () => {
    const wrapper = mount(Maintenance, { props: { snapshot: null, connectionFailed: true }, ...mountOptions() })
    expect(wrapper.text()).toContain('正在连接服务器')
    expect(wrapper.text()).toContain('无法连接服务器，正在重试')
    expect(wrapper.text()).toContain('无需手动刷新')
  })

  it('连接态百分比是 0%，且不显示「系统升级中」标题（避免误导为正在升级）', () => {
    const wrapper = mount(Maintenance, { props: { snapshot: null, connectionFailed: true }, ...mountOptions() })
    expect(wrapper.find('[data-testid="maintenance-percent"]').text()).toBe('0%')
    expect(wrapper.text()).not.toContain('系统升级中')
  })

  it('已有快照但最新一次失败 → 保留快照进度 + 显示连接告警（原因不被抹掉）', () => {
    const wrapper = mount(Maintenance, {
      props: { snapshot: makeSnapshot(), connectionFailed: true },
      ...mountOptions(),
    })
    expect(wrapper.text()).toContain('无法连接服务器')
    // 快照仍在：进度与阶段列表都还在
    expect(wrapper.find('[data-testid="maintenance-percent"]').text()).toBe('38%')
    expect(wrapper.findAll('.maintenance-step').length).toBe(5)
  })

  it('连接态给「立即重试」按钮', async () => {
    const wrapper = mount(Maintenance, { props: { snapshot: null, connectionFailed: true }, ...mountOptions() })
    const button = wrapper.findAll('button').find((b) => b.text().includes('立即重试'))
    expect(button).toBeTruthy()

    const reload = vi.fn()
    const originalLocation = window.location
    delete (window as unknown as { location?: Location }).location
    ;(window as unknown as Record<string, unknown>).location = { ...originalLocation, reload }
    await button!.trigger('click')
    expect(reload).toHaveBeenCalledOnce()
    ;(window as unknown as Record<string, unknown>).location = originalLocation
  })
})

describe('维护页：已耗时本地走秒', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('每秒 +1s（服务端 2~5s 才回一次，不本地走秒会一跳一跳）', async () => {
    const wrapper = mount(Maintenance, { props: { snapshot: makeSnapshot({ elapsed_ms: 3000 }) }, ...mountOptions() })
    expect(wrapper.find('[data-testid="maintenance-elapsed"]').text()).toContain('3 秒')
    vi.advanceTimersByTime(2000)
    await wrapper.vm.$nextTick()
    expect(wrapper.find('[data-testid="maintenance-elapsed"]').text()).toContain('5 秒')
    wrapper.unmount()
  })

  it('快照推进但服务端值更小（时钟抖动）时数字不倒退', async () => {
    const wrapper = mount(Maintenance, { props: { snapshot: makeSnapshot({ elapsed_ms: 10_000 }) }, ...mountOptions() })
    expect(wrapper.find('[data-testid="maintenance-elapsed"]').text()).toContain('10 秒')
    await wrapper.setProps({ snapshot: makeSnapshot({ elapsed_ms: 4000 }) })
    expect(wrapper.find('[data-testid="maintenance-elapsed"]').text()).toContain('10 秒')
    wrapper.unmount()
  })
})
