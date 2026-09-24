import { describe, expect, it, vi, beforeEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import NotificationDeliveries from '../NotificationDeliveries.vue'
import { notificationChannelApi } from '@/api/notificationChannel'
import { ElTableStub, ElTableColumnStub, makeChannel, makeDelivery } from './deliveryFixtures'

// G7：本页现在消费 `?channel_id=` 深链（由通知通道页「测试」后跳入），
// 故需 vue-router 替身。真实 vue-router 的 route 一定有 query，替身必须给全。
const { mockRouteQuery } = vi.hoisted(() => ({ mockRouteQuery: { value: {} as Record<string, unknown> } }))
vi.mock('vue-router', () => ({
  useRoute: () => ({ query: mockRouteQuery.value, params: {}, name: 'NotificationDeliveries', path: '/notification-deliveries' }),
  useRouter: () => ({ push: vi.fn() }),
}))

/**
 * 投递审计页的验收（P2-A 前半：列表 + channel_id/state 过滤 + 真分页）。
 *
 * 1. **真分页**：断言的是**API 收到的请求参数**（page/page_size），不是"渲染出了分页控件"
 *    —— 后者在本地 slice 的假分页下同样成立，是典型假绿。
 * 2. **过滤走服务端**：切状态/通道后，channel_id/state 出现在请求参数里，
 *    且**没有**对 items 做本地过滤（本地过滤 = 只看当前页，会伪造空态）。
 * 3. **过滤变化重置 page=1**（规范 §3.2.6 MUST）。
 * 4. **失败行能看出为什么失败**，且页面不渲染任何凭据。
 */

vi.mock('@/api/notificationChannel', () => ({
  notificationChannelApi: { listDeliveries: vi.fn(), list: vi.fn() },
}))

const mockedApi = vi.mocked(notificationChannelApi)

function mountPage() {
  return mount(NotificationDeliveries, {
    global: {
      plugins: [createPinia()],
      // 页面用了 v-loading（EP 指令）：测试环境没有 EP 插件，注册空指令消除
      // "Failed to resolve directive" 噪声，不改变任何断言。
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

/** 首屏两次请求（deliveries 列表 + 通道名下拉）的调用序号：listDeliveries 是列表那次。 */
const listCalls = () => mockedApi.listDeliveries.mock.calls

beforeEach(() => {
  vi.clearAllMocks()
  // query 是可变对象，clearAllMocks 不会重置它 —— 不重置会让 G7 深链用例相互污染
  mockRouteQuery.value = {}
  mockedApi.listDeliveries.mockResolvedValue({
    items: [makeDelivery()],
    total: 45,
    page: 1,
    page_size: 20,
  })
  mockedApi.list.mockResolvedValue({ items: [makeChannel()], total: 1, page: 1, page_size: 200 })
})

describe('NotificationDeliveries 投递审计页', () => {
  it('首屏以 page=1 / page_size=20 请求后端（默认值显式发出，不依赖后端默认）', async () => {
    mountPage()
    await flushPromises()

    expect(listCalls()).toHaveLength(1)
    expect(mockedApi.listDeliveries).toHaveBeenCalledWith({ page: 1, page_size: 20 })
    // 不带任何过滤键：undefined 键不得出现在 params 里（空串 state 会让后端 400）
    expect(Object.keys(listCalls()[0][0] as object)).toEqual(['page', 'page_size'])
  })

  it('翻到第 2 页时真的以 page=2 请求后端（真分页，不是本地切片）', async () => {
    const wrapper = mountPage()
    await flushPromises()

    // 分母守卫：首屏确实发过一次 page=1，否则下面的 toHaveBeenLastCalledWith 可能只是
    // "恰好第一次调用就是 page=2"这种不可能成立的情形。
    expect(mockedApi.listDeliveries).toHaveBeenCalledWith({ page: 1, page_size: 20 })

    const pager = wrapper.find('[data-test="nd-pagination"]')
    expect(pager.exists()).toBe(true)
    await pager.trigger('click')
    await flushPromises()

    // 核心断言：**API 收到的参数**里有 page=2（本地 slice 的假分页不会有第二次请求）。
    expect(listCalls()).toHaveLength(2)
    expect(mockedApi.listDeliveries).toHaveBeenLastCalledWith({ page: 2, page_size: 20 })
  })

  it('切换状态过滤 → 以 state= 请求后端，并重置 page=1（规范 §3.2.6 MUST）', async () => {
    const wrapper = mountPage()
    await flushPromises()
    // 先翻到第 2 页，再改过滤条件 —— 这样才能证明"重置"真的发生
    await wrapper.find('[data-test="nd-pagination"]').trigger('click')
    await flushPromises()
    expect(mockedApi.listDeliveries).toHaveBeenLastCalledWith({ page: 2, page_size: 20 })

    const select = wrapper.find('[data-test="nd-filter-state"]')
    expect(select.exists()).toBe(true)
    await select.setValue('failed')
    await flushPromises()

    // 同时钉两件事：state 真的发给了后端；页码回到了 1（否则会请求 page=2 的空页）
    expect(mockedApi.listDeliveries).toHaveBeenLastCalledWith({ state: 'failed', page: 1, page_size: 20 })
  })

  // G7：从通知通道页「测试」跳入时带 `?channel_id=`，本页必须落成**已筛该通道**的首屏，
  // 否则用户到了审计页还得自己再选一次通道（正是 G7 要消除的那一步）。
  it('G7：带 ?channel_id= 进入时首屏即以该通道过滤（无需用户再选一次）', async () => {
    mockRouteQuery.value = { channel_id: '1' }
    mountPage()
    await flushPromises()
    expect(mockedApi.listDeliveries).toHaveBeenLastCalledWith({ channel_id: 1, page: 1, page_size: 20 })
  })

  it('G7：?channel_id= 非法值被忽略（不得把 NaN 发给后端）', async () => {
    mockRouteQuery.value = { channel_id: 'abc' }
    mountPage()
    await flushPromises()
    const lastCall = mockedApi.listDeliveries.mock.calls.at(-1)?.[0] as Record<string, unknown>
    expect(lastCall.channel_id).toBeUndefined()
    expect(lastCall.page).toBe(1)
  })

  it('切换通道过滤 → 以 channel_id= 请求后端（数字，不是字符串 label）', async () => {    const wrapper = mountPage()
    await flushPromises()

    const select = wrapper.find('[data-test="nd-filter-channel"]')
    expect(select.exists()).toBe(true)
    // 必须用**字符串** '1'：test-setup.ts 的 el-select 替身是真实 <select>，
    // setValue(number) 不会写进 DOM 的 value，change 处理器读到的仍是空串（等于"不过滤"）。
    // 替身会把数字形态的字符串归一化回 number，因此下面断言的 channel_id 仍是**数字 1**。
    await select.setValue('1')
    await flushPromises()

    expect(mockedApi.listDeliveries).toHaveBeenLastCalledWith({ channel_id: 1, page: 1, page_size: 20 })
  })

  it('过滤**不**在本地裁剪 items：后端返回什么就渲染什么（本地过滤会伪造空态）', async () => {
    // 后端在 state=failed 下返回一行 delivered（模拟"后端过滤语义与前端预期不同"）：
    // 页面必须照实渲染，而不是自作主张把它过滤掉——本地过滤只能筛当前页，是假检索。
    mockedApi.listDeliveries.mockResolvedValue({
      items: [makeDelivery({ id: 77, state: 'delivered' })],
      total: 1,
      page: 1,
      page_size: 20,
    })
    const wrapper = mountPage()
    await flushPromises()
    await wrapper.find('[data-test="nd-filter-state"]').setValue('failed')
    await flushPromises()

    // 行数断言走替身暴露的 data-row-count（见 deliveryFixtures 的说明：本替身
    // 不产出 <tbody><tr>，数行数必须读它拿到的 data 长度）。
    const table = wrapper.find('[data-test="nd-table"]')
    expect(table.attributes('data-row-count')).toBe('1')
    // 后端返回的那一行必须**真的渲染出来**（本地裁剪会让它消失、只剩空态）。
    expect(wrapper.find('[data-test="nd-empty"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('77')
  })

  it('失败行渲染出 HTTP 状态码与 error_message（人能看到为什么失败）', async () => {
    mockedApi.listDeliveries.mockResolvedValue({
      items: [makeDelivery({ id: 91, state: 'failed', status_code: 500, error_message: '接收端返回 500' })],
      total: 1,
      page: 1,
      page_size: 20,
    })
    const wrapper = mountPage()
    await flushPromises()

    const text = wrapper.text()
    expect(text).toContain('失败')
    expect(text).toContain('HTTP 500')
    expect(text).toContain('接收端返回 500')
  })

  it('pending 行明确说明是「结论未落库」的中间态，不得说成已送达/投递成功', async () => {
    mockedApi.listDeliveries.mockResolvedValue({
      items: [makeDelivery({ id: 92, state: 'pending', status_code: 0, error_message: '' })],
      total: 1,
      page: 1,
      page_size: 20,
    })
    const wrapper = mountPage()
    await flushPromises()

    const text = wrapper.text()
    expect(text).toContain('进行中')
    expect(text).toContain('结论尚未落库')
    // 语义守卫：pending **这一行**不得被说成"已送达"。
    // 必须限定在数据行内断言，而不是整页文本 —— 页头副标题与状态下拉的选项里
    // 都合法地出现"已送达"（那是 delivered 的标签），整页断言会把正确文案判成缺陷。
    const stateCell = wrapper.find('[data-test="nd-table"] .el-table-column[data-label="状态"]')
    expect(stateCell.exists()).toBe(true)
    expect(stateCell.text()).toContain('进行中')
    expect(stateCell.text()).not.toContain('已送达')
  })

  it('无响应（status_code=0）显示「无响应」而不是「HTTP 0」', async () => {
    mockedApi.listDeliveries.mockResolvedValue({
      items: [makeDelivery({ id: 93, state: 'failed', status_code: 0, error_message: 'dial tcp: connection refused' })],
      total: 1,
      page: 1,
      page_size: 20,
    })
    const wrapper = mountPage()
    await flushPromises()

    expect(wrapper.text()).toContain('无响应')
    expect(wrapper.text()).not.toContain('HTTP 0')
    expect(wrapper.text()).toContain('connection refused')
  })

  it('空态：无过滤时说「暂无投递记录」，有过滤时说「没有符合条件」（两种空态语义不同）', async () => {
    mockedApi.listDeliveries.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20 })
    const wrapper = mountPage()
    await flushPromises()

    const empty = () => wrapper.find('[data-test="nd-empty"]')
    expect(empty().exists()).toBe(true)
    // 分母守卫：无过滤时确实是"暂无"这一支，否则下面的"有过滤"断言可能只是同一文案又出现一次。
    expect(empty().text()).toContain('暂无投递记录')
    expect(empty().text()).not.toContain('没有符合条件')

    await wrapper.find('[data-test="nd-filter-state"]').setValue('failed')
    await flushPromises()

    // 有过滤 ⇒ 文案必须变成"没有符合条件"：空态在说"当前条件没匹配到"，
    // 而不是"系统里压根没有投递记录" —— 两者对用户的下一步动作含义完全不同。
    expect(empty().exists()).toBe(true)
    expect(empty().text()).toContain('没有符合条件')
    expect(empty().text()).not.toContain('暂无投递记录')
    // 顺带钉住：过滤确实发给了后端（否则"有过滤"的前提是假的）。
    expect(mockedApi.listDeliveries).toHaveBeenLastCalledWith({ state: 'failed', page: 1, page_size: 20 })
  })

  it('失败态：store.error 渲染成错误条，重试按钮重新发起同参数请求', async () => {
    mockedApi.listDeliveries.mockRejectedValueOnce(new Error('查询投递记录失败'))
    const wrapper = mountPage()
    await flushPromises()

    const alert = wrapper.find('[data-test="nd-error"]')
    expect(alert.exists()).toBe(true)
    expect(alert.text()).toContain('查询投递记录失败')

    const before = listCalls().length
    await wrapper.find('[data-test="nd-retry"]').trigger('click')
    await flushPromises()
    expect(listCalls().length).toBe(before + 1)
    expect(mockedApi.listDeliveries).toHaveBeenLastCalledWith({ page: 1, page_size: 20 })
  })

  it('通道名下拉加载失败**不**算页面级错误（审计记录照常展示）', async () => {
    mockedApi.list.mockRejectedValueOnce(new Error('通道列表加载失败'))
    const wrapper = mountPage()
    await flushPromises()

    expect(wrapper.find('[data-test="nd-error"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="nd-channels-note"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="nd-table"]').attributes('data-row-count')).toBe('1')
  })

  it('F8：表格渲染在 .mobile-table-wrapper 内并带横滑提示（§4.3.2.2 MUST）', async () => {
    const wrapper = mountPage()
    await flushPromises()

    const table = wrapper.find('table.el-table').element
    expect(table.closest('.mobile-table-wrapper'), 'el-table 不在 .mobile-table-wrapper 祖先链上').not.toBeNull()
    const hint = wrapper.find('.mobile-table-wrapper > .mobile-table-hint')
    expect(hint.exists()).toBe(true)
  })

  it('页面不渲染任何凭据字段（审计行里没有 secret；页面也不拼 URL/密钥）', async () => {
    mockedApi.listDeliveries.mockResolvedValue({
      items: [makeDelivery({ state: 'failed', status_code: 401, error_message: 'unauthorized' })],
      total: 1,
      page: 1,
      page_size: 20,
    })
    const wrapper = mountPage()
    await flushPromises()

    const text = wrapper.text()
    expect(text).not.toContain('secret')
    expect(text).not.toContain('key=')
    // 通道下拉里只有 #id + 名称（通道对象的 target_url 已由后端脱敏，页面也不渲染它）
    expect(text).not.toContain('qyapi.weixin.qq.com')
  })
})
