import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import FirmwareManage from '../FirmwareManage.vue'

vi.mock('@/api/firmware', () => ({
  firmwareApi: {
    getList: vi.fn(() => Promise.resolve({
      list: [
        { id: 1, version: '1.0.0', checksum: 'abc123', size_bytes: 1024, url: 'http://test/fw1.bin', created_at: '2024-01-01T00:00:00Z' },
        { id: 2, version: '1.1.0', checksum: 'def456', size_bytes: 2048, url: 'http://test/fw2.bin', created_at: '2024-01-02T00:00:00Z' },
      ],
      total: 2,
    })),
    delete: vi.fn(() => Promise.resolve()),
    update: vi.fn(() => Promise.resolve()),
    upload: vi.fn(() => Promise.resolve()),
    getDownloadUrl: vi.fn(() => 'http://test/fw.bin'),
  },
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn() }),
}))

const stubs = {
  PageHeader: { template: '<div class="page-header"><slot /><slot name="extra" /></div>' },
  'el-card': { template: '<div class="el-card"><slot /><slot name="header" /></div>' },
  'el-table': { template: '<table class="el-table"><slot /></table>' },
  'el-table-column': { template: '<col />' },
  'el-button': { template: '<button class="el-button" @click="$emit(\'click\')"><slot /><slot name="icon" /></button>' },
  'el-dialog': { template: '<div class="el-dialog" v-if="modelValue"><slot /><slot name="footer" /></div>', props: ['modelValue'] },
  'el-form': { template: '<form class="el-form"><slot /></form>' },
  'el-form-item': { template: '<div class="el-form-item"><slot /></div>' },
  'el-input': { template: '<input class="el-input" />' },
  'el-upload': { template: '<div class="el-upload"><slot /></div>' },
  'el-icon': { template: '<i class="el-icon"><slot /></i>' },
  'el-tag': { template: '<span class="el-tag"><slot /></span>' },
  'el-pagination': { template: '<div class="el-pagination" />' },
  'el-empty': { template: '<div class="el-empty"><slot /></div>' },
  'el-skeleton': { template: '<div class="el-skeleton" />' },
  'el-switch': { template: '<div class="el-switch" />' },
  // Icon components from @element-plus/icons-vue
  Upload: { template: '<i />' },
  Edit: { template: '<i />' },
  Download: { template: '<i />' },
  Delete: { template: '<i />' },
  CopyDocument: { template: '<i />' },
  CircleCheckFilled: { template: '<i />' },
}


// ─── F8 移动端宽表横滚合同（§4.3.2.2 MUST / §4.4.1 MUST） ───────────────────
// 断言的是**真实渲染出的 DOM 祖先链**，不是源码字符串包含：
// `expect(src).toContain('class="mobile-table-wrapper"')` 无法区分
// 「包住了这张表」还是「包住了另一张表 / 只写在注释里」。happy-dom 无布局引擎
// （getBoundingClientRect 恒 0），像素级可达性由真浏览器探针验收：
// frontend-shared/.tmp-probe/f8-f10-probe.mjs
function expectEveryTableWrapped(wrapper: { findAll: (s: string) => Array<{ element: Element }> }) {
  const tables = wrapper.findAll('.el-table')
  expect(tables.length, '渲染出的 el-table 数量为 0，断言会假绿').toBeGreaterThan(0)
  for (const t of tables) {
    const el = t.element as HTMLElement
    const box = el.closest('.mobile-table-wrapper')
    expect(box, 'el-table 不在 .mobile-table-wrapper 祖先链上').not.toBeNull()
    const hint = (box as HTMLElement).querySelector(':scope > .mobile-table-hint')
    expect(hint, '.mobile-table-wrapper 缺少直接子节点 .mobile-table-hint').not.toBeNull()
    expect(hint!.textContent).toContain('左右滑动')
  }
}

describe('FirmwareManage.vue', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('F8：固件表渲染在 .mobile-table-wrapper 内并带横滑提示（§4.3.2.2 MUST）', async () => {
    const wrapper = mount(FirmwareManage, { global: { stubs } })
    await flushPromises()
    expectEveryTableWrapped(wrapper)
  })

  it('renders firmware manage container', () => {
    const wrapper = mount(FirmwareManage, { global: { stubs } })
    expect(wrapper.find('.firmware-manage').exists()).toBe(true)
  })

  it('renders page header', () => {
    const wrapper = mount(FirmwareManage, { global: { stubs } })
    expect(wrapper.find('.page-header').exists()).toBe(true)
  })

  it('renders upload button', () => {
    const wrapper = mount(FirmwareManage, { global: { stubs } })
    expect(wrapper.text()).toContain('上传固件')
  })

  it('renders firmware table', async () => {
    const wrapper = mount(FirmwareManage, { global: { stubs } })
    await flushPromises()
    // After data loads, loading=false, el-table renders (inside v-else template)
    expect(wrapper.find('.el-table').exists()).toBe(true)
  })

  it('loads firmware list on mount and renders table', async () => {
    const wrapper = mount(FirmwareManage, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.el-table').exists()).toBe(true)
  })

  it('shows empty state when no firmware', async () => {
    const { firmwareApi } = await import('@/api/firmware')
    vi.mocked(firmwareApi.getList).mockResolvedValueOnce({ list: [], total: 0 } as any)
    const wrapper = mount(FirmwareManage, { global: { stubs } })
    await flushPromises()
    // EmptyState is rendered when firmwares.length === 0 (after loading completes),
    // and the table (with its header row) is not rendered at all
    expect(wrapper.find('.empty-state').exists()).toBe(true)
    expect(wrapper.text()).toContain('暂无固件数据')
    expect(wrapper.find('.el-table').exists()).toBe(false)
  })

  it('opens upload dialog on upload button click', async () => {
    const wrapper = mount(FirmwareManage, { global: { stubs } })
    const uploadBtn = wrapper.findAll('button').find(b => b.text().includes('上传固件'))
    expect(uploadBtn).toBeDefined()
    await uploadBtn!.trigger('click')
    await flushPromises()
    // Dialog should now be visible (showUploadDialog = true → modelValue = true)
    expect(wrapper.find('.el-dialog').exists()).toBe(true)
  })

  // SKIPPED: The el-table-column stub renders as <col /> and cannot forward
  // scoped slot data (row) to its #default slot. Without a full Element Plus
  // el-table implementation, the edit/delete buttons inside table column
  // scoped slots are not rendered. Verifying button presence would require
  // either real Element Plus components or a complex render-function stub
  // that properly chains scoped slots, which is brittle for unit tests.
  it.skip('renders edit and delete buttons in table rows', async () => {
    const wrapper = mount(FirmwareManage, { global: { stubs } })
    await flushPromises()
    const buttons = wrapper.findAll('button')
    const buttonTexts = buttons.map(b => b.text())
    const hasEdit = buttonTexts.some(t => t.includes('编辑'))
    const hasDelete = buttonTexts.some(t => t.includes('删除'))
    expect(hasEdit).toBe(true)
    expect(hasDelete).toBe(true)
  })

  it('renders pagination when total > 0', async () => {
    const wrapper = mount(FirmwareManage, { global: { stubs } })
    await flushPromises()
    // Mock returns total: 2, so pagination renders (v-if="total > 0")
    expect(wrapper.find('.el-pagination').exists()).toBe(true)
  })
})
