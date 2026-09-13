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

  // 360px 实测: card__body 内容宽 310px，.filter-bar 可用 270px；三个动作按钮
  // 合计 261px + 2×8px gap = 277px > 270px。.filter-bar / .filter-left 均有 flex-wrap，
  // 缺了 .filter-right 时「导入」被推到内容区左界之外（x=9.95 < 21）且祖先链
  // scrollWidth === clientWidth（真实裁切，非可滚动溢出）。
  it('toolbar action group allows wrapping so no action is clipped on narrow viewports', () => {
    const start = source.indexOf('.filter-right {')
    expect(start).toBeGreaterThan(-1)
    const block = source.slice(start, source.indexOf('}', start) + 1)
    expect(block).toContain('flex-wrap: wrap;')
  })

  it('wrapping the toolbar actions does not drop any of the three actions', async () => {
    const wrapper = mount(DeviceConfigList, { global: { stubs } })
    await flushPromises()

    const actions = wrapper.find('.filter-right').findAll('button')
    expect(actions.map(button => button.text().replace(/\s+/g, ''))).toEqual(['导入', '导出', '新建模板'])
  })
})
