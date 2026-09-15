import { describe, expect, it, vi, beforeEach } from 'vitest'
import { defineComponent, h, inject, provide } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { ElMessageBox } from 'element-plus'
import NotificationChannels from '../NotificationChannels.vue'
import { notificationChannelApi } from '@/api/notificationChannel'
import type { NotificationChannel } from '@/api/notificationChannel'

/**
 * 外发通知通道列表页的两条验收（本轮范围：列表 + 分页 + 删除）。
 *
 * 1. 分页是**真分页**：点下一页必须让 API 收到 page=2 —— 断言的是**请求参数**，
 *    不是"渲染出了分页控件"（后者在本地 slice 的假分页下同样成立，是典型假绿）。
 * 2. 删除必须走 feedback.confirmDanger：用户取消时**一个请求都不发**。
 */

vi.mock('@/api/notificationChannel', () => ({
  notificationChannelApi: { list: vi.fn(), remove: vi.fn() },
}))

// ElMessage / ElMessageBox 是 feedback 的底层依赖：这里替换成 spy，
// 使 confirmDanger 的"取消"分支可被确定性地驱动（不弹真弹窗）。
vi.mock('element-plus', async (importOriginal) => {
  const actual = await importOriginal<typeof import('element-plus')>()
  return {
    ...actual,
    ElMessage: Object.assign(vi.fn(), { success: vi.fn(), warning: vi.fn(), error: vi.fn(), info: vi.fn() }),
    ElMessageBox: { confirm: vi.fn() },
  }
})

const mockedApi = vi.mocked(notificationChannelApi)
const confirmSpy = vi.mocked(ElMessageBox.confirm)

/**
 * test-setup.ts 的通用 el-table stub **不渲染列的行作用域插槽**（它只把整行值串成一格），
 * 因此行内按钮取不到。这里用 provide/inject 版替身：表提供 rows，列按行渲染自己的
 * scoped slot —— 这是本文件能点到「删除」按钮的前提。
 */
const ROWS = Symbol('rows')
const ElTableStub = defineComponent({
  name: 'ElTable',
  props: { data: { type: Array, default: () => [] } },
  setup(props, { slots }) {
    provide(ROWS, props)
    return () => h('table', { class: 'el-table' }, slots.default?.())
  },
})
const ElTableColumnStub = defineComponent({
  name: 'ElTableColumn',
  props: {
    label: String,
    prop: String,
    width: [String, Number],
    minWidth: [String, Number],
    fixed: [String, Boolean],
  },
  setup(props, { slots }) {
    const table = inject<{ data: unknown[] } | null>(ROWS, null)
    return () => {
      const rows = table?.data ?? []
      const cell = slots.default
        ? rows.map((row, index) => slots.default!({ row, $index: index }))
        : [h('span', String(props.label ?? ''))]
      return h('td', { class: 'el-table-column', 'data-label': props.label }, cell)
    }
  },
})

function makeChannel(overrides: Partial<NotificationChannel> = {}): NotificationChannel {
  return {
    id: 1,
    name: '运维群',
    type: 'wecom',
    target_url: 'https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=***',
    secret_hint: 'abcd',
    has_secret: true,
    template: '',
    min_level: 'warning',
    enabled: true,
    timeout_sec: null,
    max_retries: 0,
    allow_private: false,
    created_at: '2026-09-01T00:00:00Z',
    updated_at: '2026-09-01T00:00:00Z',
    ...overrides,
  }
}

function mountPage() {
  return mount(NotificationChannels, {
    global: {
      plugins: [createPinia()],
      // 页面用了 v-loading（EP 指令，本仓 12 处既有用法）：测试环境没有 EP 插件，
      // 注册一个空指令即可消除 "Failed to resolve directive" 噪声，不改变任何断言。
      directives: { loading: {} },
      components: {
        ElTable: ElTableStub,
        'el-table': ElTableStub,
        ElTableColumn: ElTableColumnStub,
        'el-table-column': ElTableColumnStub,
      },
    },
  })
}

beforeEach(() => {
  vi.clearAllMocks()
  // 默认返回 25 条总量（>1 页，分页控件才存在）与一页数据。
  mockedApi.list.mockResolvedValue({
    items: [makeChannel()],
    total: 25,
    page: 1,
    page_size: 20,
  })
  mockedApi.remove.mockResolvedValue({ id: 1, deleted: true, deliveries_retained: true })
  confirmSpy.mockResolvedValue('confirm' as never)
})

describe('NotificationChannels 列表页', () => {
  it('翻到第 2 页时真的以 page=2 请求后端（真分页，不是本地切片）', async () => {
    const wrapper = mountPage()
    await flushPromises()

    // 分母守卫：首屏确实发过一次 page=1，否则下面的 toHaveBeenLastCalledWith 可能只是
    // "恰好第一次调用就是 page=2"这种不可能成立的情形 —— 这里显式钉住首屏语义。
    expect(mockedApi.list).toHaveBeenCalledTimes(1)
    expect(mockedApi.list).toHaveBeenCalledWith({ page: 1, page_size: 20 })

    // el-pagination 的 data-test 会 fallthrough 到它的根 <button>，故选它本身。
    const pager = wrapper.find('[data-test="nc-pagination"]')
    expect(pager.exists()).toBe(true)
    await pager.trigger('click')
    await flushPromises()

    // 核心断言：**API 收到的参数**里有 page=2（本地 slice 的假分页不会有第二次请求）。
    expect(mockedApi.list).toHaveBeenCalledTimes(2)
    expect(mockedApi.list).toHaveBeenLastCalledWith({ page: 2, page_size: 20 })
  })

  it('删除走 confirmDanger；用户取消时不发任何删除请求', async () => {
    // 取消 = ElMessageBox.confirm reject（feedback.confirmDanger 的 catch 分支）
    confirmSpy.mockRejectedValue(new Error('cancel'))

    const wrapper = mountPage()
    await flushPromises()

    const deleteBtn = wrapper.find('[data-test="nc-delete"]')
    expect(deleteBtn.exists()).toBe(true)
    await deleteBtn.trigger('click')
    await flushPromises()

    // 分母守卫：确认框**确实弹过**（否则"没发请求"可能只是因为按钮压根没接上事件）。
    expect(confirmSpy).toHaveBeenCalledTimes(1)
    expect(String(confirmSpy.mock.calls[0][0])).toContain('运维群')

    // 取消分支的全部含义：一个删除请求都不发。
    expect(mockedApi.remove).not.toHaveBeenCalled()
  })
})
