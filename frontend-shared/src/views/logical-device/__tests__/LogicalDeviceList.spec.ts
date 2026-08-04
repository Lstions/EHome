import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import LogicalDeviceList from '@/views/logical-device/LogicalDeviceList.vue'

// ── Mocks ──────────────────────────────────────────────

const { mockPush, mockRoute } = vi.hoisted(() => ({
  mockPush: vi.fn(),
  mockRoute: { path: '/logical-device', query: {} as Record<string, string> },
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: mockPush }),
  useRoute: () => mockRoute,
}))

vi.mock('element-plus', () => ({
  ElMessage: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}))

// 保留真实 extractMergeConflicts (结构化 409 解析), 仅 mock API 方法。
vi.mock('@/api/logicalDevice', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/logicalDevice')>()
  return {
    ...actual,
    logicalDeviceApi: {
      list: vi.fn(),
      mergePreview: vi.fn(),
      merge: vi.fn(),
      mergeJob: vi.fn(),
      update: vi.fn(),
    },
  }
})

import { logicalDeviceApi } from '@/api/logicalDevice'
import { ElMessage } from 'element-plus'

const mockList = vi.mocked(logicalDeviceApi.list)
const mockPreview = vi.mocked(logicalDeviceApi.mergePreview)
const mockMerge = vi.mocked(logicalDeviceApi.merge)
const mockMergeJob = vi.mocked(logicalDeviceApi.mergeJob)
const mockUpdate = vi.mocked(logicalDeviceApi.update)

// ── Fixtures ───────────────────────────────────────────

const makeItem = (over: Partial<any> = {}) => ({
  id: 3,
  identity_key: 'bms:0x76',
  name: '客厅BMS',
  device_type: 'bms',
  retention_days: 365,
  merged_into: null,
  merge_status: null,
  purge_requested: false,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
  instance_count: 1,
  row_estimate: 12847,
  last_data_at: '2026-08-04T08:00:00Z',
  ...over,
})

const mountPage = () =>
  mount(LogicalDeviceList, {
    global: {
      stubs: {
        // 图标无交互语义, stub 掉避免解析告警
        Connection: true,
        Refresh: true,
        InfoFilled: true,
        WarningFilled: true,
      },
    },
  })

describe('LogicalDeviceList.vue', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockRoute.query = {}
    mockList.mockResolvedValue({ items: [], total: 0 })
    mockPreview.mockResolvedValue({ sources: [], target_retention_days: 365 })
    mockMerge.mockResolvedValue({ target_id: 9, job_ids: [11] })
    mockMergeJob.mockResolvedValue({
      id: 11,
      source_logical_id: 3,
      target_logical_id: 9,
      status: 'done',
      migrated_rows: 100,
      total_estimate: 100,
      watermark_id: 0,
      watermark_phase: 'unified_data',
      retry_count: 0,
      created_at: '',
      updated_at: '',
      finished_at: null,
    })
    mockUpdate.mockImplementation(async (_id, updates) => makeItem(updates))
  })

  // ─── 列表渲染 ───

  it('renders list items with name/type/instance count/retention', async () => {
    mockList.mockResolvedValue({
      items: [makeItem(), makeItem({ id: 5, name: '卧室BMS', instance_count: 2 })],
      total: 2,
    })
    const wrapper = mountPage()
    await flushPromises()

    expect(mockList).toHaveBeenCalledTimes(1)
    const text = wrapper.text()
    expect(text).toContain('客厅BMS')
    expect(text).toContain('卧室BMS')
    // 表格行内合并值含实例数/保留天数/数据量
    expect(text).toContain('365')
  })

  it('marks merged/pending/purge devices with status tags (源码约定)', async () => {
    // el-table stub 不渲染 scoped slot, 状态标签模板用源码断言锁定。
    const src = (await import('@/views/logical-device/LogicalDeviceList.vue?raw')).default
    expect(src).toContain("row.merge_status === 'pending'")
    expect(src).toContain("row.merge_status === 'done'")
    expect(src).toContain('row.purge_requested')
    expect(src).toContain('已合并 →')
  })

  it('filters by search keyword', async () => {
    mockList.mockResolvedValue({
      items: [makeItem({ id: 3, name: '客厅BMS' }), makeItem({ id: 5, name: '卧室BMS' })],
      total: 2,
    })
    const wrapper = mountPage()
    await flushPromises()

    const search = wrapper.find('input[placeholder="搜索逻辑设备名称..."]')
    expect(search.exists()).toBe(true)
    await search.setValue('卧室')
    await flushPromises()

    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(1)
    expect(rows[0].text()).toContain('卧室BMS')
  })

  // ─── 合并门控 ───

  it('disables merge button until 2+ same-type sources selected', async () => {
    mockList.mockResolvedValue({
      items: [
        makeItem({ id: 3, name: 'A', device_type: 'bms' }),
        makeItem({ id: 5, name: 'B', device_type: 'bms' }),
        makeItem({ id: 7, name: 'C', device_type: 'sn3001' }),
      ],
      total: 3,
    })
    const wrapper = mountPage()
    await flushPromises()

    const mergeButton = () =>
      wrapper.findAll('button').find(b => b.text().includes('合并所选'))!
    const checkboxes = wrapper.findAll('input.el-table__row-checkbox')
    expect(checkboxes).toHaveLength(3)

    // 0 选: 禁用
    expect(mergeButton().attributes('disabled')).toBeDefined()

    // 1 选: 仍禁用
    await checkboxes[0].setValue(true)
    expect(mergeButton().attributes('disabled')).toBeDefined()

    // 2 个同 type: 可用
    await checkboxes[1].setValue(true)
    expect(mergeButton().attributes('disabled')).toBeUndefined()

    // 换成跨 type 组合 (A + C): 再次禁用
    await checkboxes[1].setValue(false)
    await checkboxes[2].setValue(true)
    expect(mergeButton().attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('所选逻辑设备必须属于同一设备类型')
  })

  // ─── 预览 + 确认合并 ───

  it('opens preview with time ranges/overlap/target retention, then confirms merge', async () => {
    mockList.mockResolvedValue({
      items: [
        makeItem({ id: 3, name: 'A', device_type: 'bms' }),
        makeItem({ id: 5, name: 'B', device_type: 'bms' }),
      ],
      total: 2,
    })
    mockPreview.mockResolvedValue({
      sources: [
        {
          id: 3, name: 'A', device_type: 'bms',
          first_data_at: '2026-07-01T00:00:00Z', last_data_at: '2026-07-20T00:00:00Z',
          row_estimate: 1000, overlap_with_others: true,
        },
        {
          id: 5, name: 'B', device_type: 'bms',
          first_data_at: '2026-07-15T00:00:00Z', last_data_at: '2026-08-01T00:00:00Z',
          row_estimate: 2000, overlap_with_others: true,
        },
      ],
      target_retention_days: 365,
    })

    const wrapper = mountPage()
    await flushPromises()

    const checkboxes = wrapper.findAll('input.el-table__row-checkbox')
    await checkboxes[0].setValue(true)
    await checkboxes[1].setValue(true)

    const mergeButton = wrapper.findAll('button').find(b => b.text().includes('合并所选'))!
    await mergeButton.trigger('click')
    await flushPromises()

    expect(mockPreview).toHaveBeenCalledWith([3, 5])
    const dialogText = wrapper.find('.el-dialog').text()
    expect(dialogText).toContain('A')
    expect(dialogText).toContain('B')
    expect(dialogText).toContain('重叠')
    expect(dialogText).toContain('365')
    // 数据量合计 = 1000 + 2000
    expect(dialogText).toContain('3.0k')

    // 目标名称默认拼接源名, 可编辑
    const targetInput = wrapper.find('input[data-testid="merge-target-name"]')
    expect((targetInput.element as HTMLInputElement).value).toBe('A + B')
    await targetInput.setValue('合并后的BMS')

    const confirm = wrapper.find('button[data-testid="merge-confirm"]')
    await confirm.trigger('click')
    await flushPromises()

    expect(mockMerge).toHaveBeenCalledWith('合并后的BMS', [3, 5])
    expect(ElMessage.success).toHaveBeenCalledWith('合并已发起，后台正在搬迁数据')
    // 进度弹窗打开; mergeJob mock 返回 done → 首轮轮询即收敛并刷新列表
    await flushPromises()
    expect(mockMergeJob).toHaveBeenCalled()
  })

  // ─── 409 conflicts ───

  it('renders 409 conflicts item-by-item with instance jump', async () => {
    mockList.mockResolvedValue({
      items: [
        makeItem({ id: 3, name: 'A', device_type: 'bms' }),
        makeItem({ id: 5, name: 'B', device_type: 'bms' }),
      ],
      total: 2,
    })
    mockMerge.mockRejectedValue({
      status: 409,
      message: '合并校验未通过',
      response: {
        data: {
          code: 409,
          message: '合并校验未通过',
          conflicts: [
            {
              logical_device_id: 3, logical_name: 'A', reason: 'alive_instance',
              instance_id: 17, instance_name: 'BMS-1', node_name: '客厅采集器',
            },
            { logical_device_id: 5, logical_name: 'B', reason: 'purge_requested' },
          ],
        },
      },
    })

    const wrapper = mountPage()
    await flushPromises()

    const checkboxes = wrapper.findAll('input.el-table__row-checkbox')
    await checkboxes[0].setValue(true)
    await checkboxes[1].setValue(true)
    await wrapper.findAll('button').find(b => b.text().includes('合并所选'))!.trigger('click')
    await flushPromises()
    // 预览弹窗 → 确认
    await wrapper.find('button[data-testid="merge-confirm"]').trigger('click')
    await flushPromises()

    const conflictDialog = wrapper.findAll('.el-dialog').find(d => d.text().includes('合并被拒绝'))!
    expect(conflictDialog.text()).toContain('仍有存活实例')
    expect(conflictDialog.text()).toContain('BMS-1')
    expect(conflictDialog.text()).toContain('客厅采集器')
    expect(conflictDialog.text()).toContain('已标记删除数据')

    // 实例跳转
    const jump = conflictDialog.find('button[data-testid="conflict-jump"]')
    expect(jump.exists()).toBe(true)
    await jump.trigger('click')
    expect(mockPush).toHaveBeenCalledWith('/edge-device/17')
  })

  // ─── 编辑 (retention 深链打开) ───

  it('opens edit dialog via ?retention=<id> deep link and saves name/retention', async () => {
    mockList.mockResolvedValue({
      items: [makeItem({ id: 7, name: '待延期设备', retention_days: 30 })],
      total: 1,
    })
    mockRoute.query = { retention: '7' }

    const wrapper = mountPage()
    await flushPromises()

    // 深链自动打开编辑弹窗; 名称回显在 input value 上 (不在 textContent 中)
    const dialog = wrapper.findAll('.el-dialog').find(d => d.text().includes('编辑逻辑设备'))!
    expect(dialog.exists()).toBe(true)

    const nameInput = wrapper.find('input[data-testid="edit-name"]')
    expect((nameInput.element as HTMLInputElement).value).toBe('待延期设备')
    await nameInput.setValue('延期后的设备')
    const retentionInput = wrapper.find('input[data-testid="edit-retention"]')
    await retentionInput.setValue('730')

    await wrapper.find('button[data-testid="edit-save"]').trigger('click')
    await flushPromises()

    expect(mockUpdate).toHaveBeenCalledWith(7, { name: '延期后的设备', retention_days: 730 })
    expect(ElMessage.success).toHaveBeenCalledWith('已保存')
  })

  it('rejects empty name on save', async () => {
    mockList.mockResolvedValue({ items: [makeItem({ id: 7 })], total: 1 })
    mockRoute.query = { retention: '7' }

    const wrapper = mountPage()
    await flushPromises()

    await wrapper.find('input[data-testid="edit-name"]').setValue('   ')
    await wrapper.find('button[data-testid="edit-save"]').trigger('click')
    await flushPromises()

    expect(mockUpdate).not.toHaveBeenCalled()
    expect(ElMessage.warning).toHaveBeenCalledWith('名称不能为空')
  })

  // ─── 进度轮询 ───

  it('shows failed job status and warning when migration fails', async () => {
    mockList.mockResolvedValue({
      items: [
        makeItem({ id: 3, name: 'A', device_type: 'bms' }),
        makeItem({ id: 5, name: 'B', device_type: 'bms' }),
      ],
      total: 2,
    })
    mockMerge.mockResolvedValue({ target_id: 9, job_ids: [11, 12] })
    mockMergeJob.mockImplementation(async (id) => ({
      id,
      source_logical_id: id === 11 ? 3 : 5,
      target_logical_id: 9,
      status: id === 11 ? 'done' : 'failed',
      migrated_rows: 100,
      total_estimate: 100,
      watermark_id: 0,
      watermark_phase: 'unified_data',
      retry_count: id === 12 ? 3 : 0,
      created_at: '',
      updated_at: '',
      finished_at: null,
    }) as any)

    const wrapper = mountPage()
    await flushPromises()

    const checkboxes = wrapper.findAll('input.el-table__row-checkbox')
    await checkboxes[0].setValue(true)
    await checkboxes[1].setValue(true)
    await wrapper.findAll('button').find(b => b.text().includes('合并所选'))!.trigger('click')
    await flushPromises()
    await wrapper.find('button[data-testid="merge-confirm"]').trigger('click')
    await flushPromises()

    expect(ElMessage.warning).toHaveBeenCalledWith(expect.stringContaining('1 个任务失败'))
    const progressDialog = wrapper.findAll('.el-dialog').find(d => d.text().includes('数据搬迁进度'))!
    expect(progressDialog.text()).toContain('完成')
    expect(progressDialog.text()).toContain('失败')
    expect(progressDialog.text()).toContain('重试 3 次')
  })
})
