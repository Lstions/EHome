import { describe, expect, it, vi, beforeEach } from 'vitest'
import { defineComponent, h, inject, provide } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { ElMessageBox } from 'element-plus'
import NotificationChannels from '../NotificationChannels.vue'
import NotificationChannelFormDialog from '@/components/notification/NotificationChannelFormDialog.vue'
import { notificationChannelApi } from '@/api/notificationChannel'
import type { NotificationChannel } from '@/api/notificationChannel'

/**
 * 切片二（表单 + secret 语义 + 预设模板 + 测试按钮）的四条验收 + 一条补充。
 *
 * 断言对象的选择是刻意的：四条里有三条断言的是**发给后端的请求体**，而不是
 * "界面上有个输入框 / 有个下拉选项" —— 后者在"永远不传 secret"或"永远传空串"
 * 的假实现下同样成立（本仓"假绿"纪律）。
 *
 * secret 的三态来自后端冻结契约（handler_notification_channel.go:389-396）：
 *   req.Secret == nil → 不改；!= nil → 写入，"" 表示清空。
 */

vi.mock('@/api/notificationChannel', () => ({
  notificationChannelApi: {
    list: vi.fn(), remove: vi.fn(), create: vi.fn(), update: vi.fn(), test: vi.fn(),
  },
}))

// ElMessage / ElMessageBox 是 feedback 的底层依赖：换成 spy，避免测试里弹真 toast。
vi.mock('element-plus', async (importOriginal) => {
  const actual = await importOriginal<typeof import('element-plus')>()
  return {
    ...actual,
    ElMessage: Object.assign(vi.fn(), { success: vi.fn(), warning: vi.fn(), error: vi.fn(), info: vi.fn() }),
    ElMessageBox: { confirm: vi.fn() },
  }
})

const mockedApi = vi.mocked(notificationChannelApi)

/**
 * test-setup.ts 的通用 el-table stub 不渲染列的行作用域插槽（行内按钮取不到）。
 * 这里用 provide/inject 版替身渲染每行的 scoped slot —— 点到「编辑 / 测试」的前提。
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
    // 末 4 位提示：绝不能被当作 secret 提交（这是本文件第 1 条的真正考点）
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

function mountPage(): VueWrapper {
  return mount(NotificationChannels, {
    global: {
      plugins: [createPinia()],
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

function mountDialog(channel: NotificationChannel | null): VueWrapper {
  return mount(NotificationChannelFormDialog, {
    props: { visible: true, channel },
    global: { plugins: [createPinia()] },
  })
}

/** 打开列表页 → 点该行的「编辑」→ 打开编辑对话框。 */
async function openEditDialog(): Promise<VueWrapper> {
  const wrapper = mountPage()
  await flushPromises()
  const editBtn = wrapper.find('[data-test="nc-edit"]')
  expect(editBtn.exists(), '行内「编辑」按钮不存在 —— 断言会失去对象（分母为 0）').toBe(true)
  await editBtn.trigger('click')
  await flushPromises()
  expect(wrapper.find('[data-test="nc-dialog"]').exists(), '编辑对话框没打开').toBe(true)
  return wrapper
}

/** 取出页面提交给后端的请求体（update 或 create 的第二个参数）。 */
function submittedPayload(): Record<string, unknown> {
  const call = mockedApi.update.mock.calls[0] ?? mockedApi.create.mock.calls[0]
  expect(call, '页面一个写请求都没发 —— 请求体断言会假绿').toBeTruthy()
  return call[1] as Record<string, unknown>
}

beforeEach(() => {
  vi.clearAllMocks()
  mockedApi.list.mockResolvedValue({ items: [makeChannel()], total: 1, page: 1, page_size: 20 })
  mockedApi.update.mockResolvedValue(makeChannel())
  mockedApi.create.mockResolvedValue(makeChannel({ id: 2 }))
  mockedApi.test.mockResolvedValue({
    channel_id: 1, notification_id: 9, state: 'pending', deliveries_url: '/api/v1/notification-deliveries?channel_id=1',
  })
  vi.mocked(ElMessageBox.confirm).mockResolvedValue('confirm' as never)
})

describe('通知通道表单：secret 语义 + 预设模板 + 测试按钮', () => {
  it('① 编辑时密钥留空 ⇒ 请求体不含 secret 键（后端"未传 = 不改"契约）', async () => {
    const wrapper = await openEditDialog()

    // 密钥输入框确实存在且为空（不是"没找到输入框所以没传值"）
    const secretInput = wrapper.find('[data-test="field-secret"]')
    expect(secretInput.exists(), '密钥输入框不存在').toBe(true)
    expect((secretInput.element as HTMLInputElement).value).toBe('')

    await wrapper.find('[data-test="nc-submit"]').trigger('click')
    await flushPromises()

    expect(mockedApi.update).toHaveBeenCalledTimes(1)
    const payload = submittedPayload()

    // ★ 核心断言一：secret **键根本不存在**（不是 undefined 值，是键不出现）
    expect(Object.keys(payload)).not.toContain('secret')
    // ★ 核心断言二：末 4 位提示绝不能被当作 secret 提交回去
    expect(JSON.stringify(payload)).not.toContain('abcd')
    // 分母守卫：其余字段真的提交了 —— 否则"没有 secret 键"可能只是因为压根没提交
    expect(payload.name).toBe('运维群')
    expect(payload.target_url).toContain('qyapi.weixin.qq.com')
    // 编辑态 id 必须来自被编辑的行
    expect(mockedApi.update.mock.calls[0][0]).toBe(1)
  })

  it('② 编辑时密钥填了内容 ⇒ 请求体含该内容（对照①，防"永远不传 secret"的假实现）', async () => {
    const wrapper = await openEditDialog()

    await wrapper.find('[data-test="field-secret"]').setValue('brand-new-secret')
    await wrapper.find('[data-test="nc-submit"]').trigger('click')
    await flushPromises()

    const payload = submittedPayload()
    expect(payload.secret).toBe('brand-new-secret')
    // 填了新值就不得把 hint 混进去
    expect(JSON.stringify(payload)).not.toContain('abcd')
  })

  it('③ 选「企业微信」预设 ⇒ URL 前缀与 markdown body 被预填（断言预填后的值）', async () => {
    const wrapper = mountDialog(null)

    // 分母守卫 / 对照：切换前是空的 —— 证明下面的断言来自"选了类型"，而不是初始就有值
    const urlBefore = (wrapper.find('[data-test="field-target-url"]').element as HTMLInputElement).value
    const tplBefore = (wrapper.find('[data-test="field-template"]').element as HTMLTextAreaElement).value
    expect([urlBefore, tplBefore]).toEqual(['', ''])

    await wrapper.find('[data-test="field-type"]').setValue('wecom')
    await flushPromises()

    const url = (wrapper.find('[data-test="field-target-url"]').element as HTMLInputElement).value
    const tpl = (wrapper.find('[data-test="field-template"]').element as HTMLTextAreaElement).value
    // URL 前缀 = 设计 §2.4 表格里的企业微信群机器人地址
    expect(url).toBe('https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=')
    // body = markdown 形态，且含模板占位符（后端 notify.BuildPayload 会渲染它）
    expect(tpl).toContain('"msgtype":"markdown"')
    // 后端企微预设用 printf 包了一层（notify/template.go:88），故占位符是 .Title/.Message
    expect(tpl).toContain('.Title')
    expect(tpl).toContain('.Message')
    // 对照：不得把另一个类型的预设串进来
    expect(tpl).not.toContain('user_id')
  })

  it('④ 测试按钮调 POST /:id/test', async () => {
    const wrapper = mountPage()
    await flushPromises()

    const testBtn = wrapper.find('[data-test="nc-test"]')
    expect(testBtn.exists(), '行内「测试」按钮不存在').toBe(true)
    await testBtn.trigger('click')
    await flushPromises()

    expect(mockedApi.test).toHaveBeenCalledTimes(1)
    expect(mockedApi.test).toHaveBeenCalledWith(1)
    // 测试不是写操作：不得顺带改任何通道配置
    expect(mockedApi.update).not.toHaveBeenCalled()
  })

  /**
   * 补充（主控只要求 4 条，这条守护本轮**新增**的「清除密钥」操作）：
   * 后端把"传空串"定义为清空，而"留空"必须是不改 —— 两者只有靠一个显式操作才能区分。
   */
  it('⑤（补充）点「清除密钥」后保存 ⇒ 请求体 secret 为空串（后端语义：清空）', async () => {
    const wrapper = await openEditDialog()

    const clearBtn = wrapper.find('[data-test="nc-clear-secret"]')
    expect(clearBtn.exists(), '「清除密钥」按钮不存在（已设置密钥的通道应当显示它）').toBe(true)
    await clearBtn.trigger('click')
    await wrapper.find('[data-test="nc-submit"]').trigger('click')
    await flushPromises()

    const payload = submittedPayload()
    expect('secret' in payload).toBe(true)
    expect(payload.secret).toBe('')
  })
})
