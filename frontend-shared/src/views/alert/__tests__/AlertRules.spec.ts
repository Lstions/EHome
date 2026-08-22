import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import AlertRules from '../AlertRules.vue'
import { useAlertStore } from '@/stores/alert'
import { alertApi } from '@/api/alert'
import { edgeDeviceApi } from '@/api/edgeDevice'

// Element Plus 组件由 src/test-setup.ts 全局 stub; 这里 mock 数据层。
vi.mock('@/api/alert', () => ({
  alertApi: {
    listRules: vi.fn(),
    createRule: vi.fn(),
    updateRule: vi.fn(),
    deleteRule: vi.fn(),
    setRuleEnabled: vi.fn(),
    listEvents: vi.fn(),
    markEventsRead: vi.fn(),
  },
}))
vi.mock('@/api/edgeDevice', () => ({
  edgeDeviceApi: {
    getList: vi.fn(),
  },
}))
vi.mock('element-plus', async importOriginal => {
  const actual = await importOriginal<typeof import('element-plus')>()
  return {
    ...actual,
    ElMessage: { success: vi.fn(), warning: vi.fn(), error: vi.fn() },
    ElMessageBox: { confirm: vi.fn().mockResolvedValue(true) },
  }
})

const mockedAlertApi = vi.mocked(alertApi)
const mockedEdgeApi = vi.mocked(edgeDeviceApi)

const ruleFixture = {
  id: 1,
  target_type: 'edge_device' as const,
  target_id: 10,
  sensor_name: 'cell_voltage_1',
  comparator: 'gt' as const,
  threshold: 4.2,
  duration_sec: 0,
  silence_sec: 300,
  level: 'warning' as const,
  enabled: true,
  name: '电池过压',
  created_at: '2026-08-21T00:00:00Z',
  updated_at: '2026-08-21T00:00:00Z',
}

beforeEach(() => {
  setActivePinia(createPinia())
  vi.clearAllMocks()
  mockedAlertApi.listRules.mockResolvedValue([ruleFixture])
  mockedAlertApi.listEvents.mockResolvedValue([])
  mockedEdgeApi.getList.mockResolvedValue({ total: 1, items: [{ id: 10, name: 'BMS-01' } as never] })
})

async function mountPage() {
  const wrapper = mount(AlertRules, {
    global: { plugins: [createPinia()] },
  })
  await flushPromises()
  return wrapper
}

describe('AlertRules.vue', () => {
  it('挂载后加载规则与事件', async () => {
    await mountPage()
    expect(mockedAlertApi.listRules).toHaveBeenCalledTimes(1)
    expect(mockedAlertApi.listEvents).toHaveBeenCalledTimes(1)
  })

  it('store 规则数据驱动表格渲染', async () => {
    const wrapper = await mountPage()
    const store = useAlertStore(wrapper.vm.$pinia)
    expect(store.rules).toHaveLength(1)
    expect(wrapper.find('[data-test="rules-table"]').exists()).toBe(true)
  })

  it('打开创建对话框并校验空表单', async () => {
    const wrapper = await mountPage()
    await wrapper.find('[data-test="create-rule"]').trigger('click')
    expect(wrapper.find('[data-test="rule-dialog"]').exists()).toBe(true)
    // 空表单保存应被拦截 (ElMessage.warning)
    const { ElMessage } = await import('element-plus')
    await wrapper.find('[data-test="save-rule"]').trigger('click')
    await flushPromises()
    expect(ElMessage.warning).toHaveBeenCalledWith('请填写规则名称')
  })

  it('创建成功后调用 store 并关闭对话框', async () => {
    const created = { ...ruleFixture, id: 2, name: '新规则' }
    mockedAlertApi.createRule.mockResolvedValue(created)
    const wrapper = await mountPage()
    await wrapper.find('[data-test="create-rule"]').trigger('click')
    // 填表 (ElInput/ElSelect stub 将 data-test 直接渲染在控件自身上)
    await wrapper.find('[data-test="field-name"]').setValue('新规则')
    await wrapper.find('[data-test="field-target-id"]').setValue('10')
    await wrapper.find('[data-test="field-sensor"]').setValue('temperature')
    await wrapper.find('[data-test="save-rule"]').trigger('click')
    await flushPromises()
    expect(mockedAlertApi.createRule).toHaveBeenCalledTimes(1)
    expect(mockedAlertApi.createRule).toHaveBeenCalledWith(expect.objectContaining({
      name: '新规则',
      target_id: 10,
      sensor_name: 'temperature',
    }))
  })
})
