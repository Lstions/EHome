import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import DeviceConfigList from '@/views/config/DeviceConfigList.vue'
import source from '@/views/config/DeviceConfigList.vue?raw'

const { mockGetList } = vi.hoisted(() => ({
  mockGetList: vi.fn(() => Promise.resolve({
    list: [
      {
        id: 1,
        name: 'UART 温湿度模板',
        description: '',
        device_type: 'temp_humidity',
        hardware_type: 'uart',
        config: {},
        is_default: false,
        status: 'active',
        created_at: '',
        updated_at: '',
      },
    ],
    total: 1,
    page: 1,
    page_size: 12,
  })),
}))

vi.mock('@/api/deviceConfig', () => ({
  deviceConfigApi: {
    getList: mockGetList,
    create: vi.fn(),
    update: vi.fn(),
    delete: vi.fn(),
    setDefault: vi.fn(),
  },
}))

vi.mock('element-plus', () => ({
  ElMessage: { error: vi.fn(), success: vi.fn(), warning: vi.fn() },
  ElMessageBox: { confirm: vi.fn() },
}))

const stubs = {
  DeviceConfigForm: true,
  EmptyState: true,
  'el-card': { template: '<section class="el-card"><slot /><slot name="header" /></section>' },
  'el-icon': { template: '<i><slot /></i>' },
  'el-input': { template: '<input />' },
  'el-select': { template: '<select><slot /></select>' },
  'el-option': true,
  'el-button': { template: '<button @click="$emit(\'click\')"><slot /></button>' },
  'el-tag': { template: '<span><slot /></span>' },
  'el-dropdown': { template: '<div><slot /><slot name="dropdown" /></div>' },
  'el-dropdown-menu': true,
  'el-dropdown-item': true,
  'el-pagination': true,
  'el-empty': true,
  'el-dialog': { template: '<div><slot /><slot name="footer" /></div>' },
}

describe('DeviceConfigList.vue', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders four statistic cards after loading template statistics', async () => {
    const wrapper = mount(DeviceConfigList, { global: { stubs } })
    await flushPromises()

    expect(mockGetList).toHaveBeenCalledWith({
      device_type: undefined,
      hardware_type: undefined,
      page: 1,
      page_size: 12,
    })
    expect(wrapper.findAll('.stats-row .stat-card')).toHaveLength(4)
    expect(wrapper.text()).toContain('模板总数')
    expect(wrapper.text()).toContain('本页启用')
  })

  it('declares a four-column compact grid for mobile statistics', () => {
    expect(source).toContain('@media (max-width: 768px)')
    expect(source).toContain('.stats-row { grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 8px; }')
  })
})
