import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import DeviceInfoCard from '../DeviceInfoCard.vue'
import type { EdgeDevice } from '@/api/edgeDevice'

vi.mock('@/composables/useResponsive', () => ({
  useResponsive: () => ({
    width: { value: 1440 },
    isMobile: { value: false },
    isTablet: { value: false },
    isDesktop: { value: true },
  }),
}))

function makeDevice(overrides: Partial<EdgeDevice> = {}): EdgeDevice {
  return {
    id: 1,
    node_id: 'F0F5BDFFFE02',
    channel_id: 1,
    name: 'bms-1',
    device_type: 'jiabaida_bms',
    protocol: 'modbus',
    hardware_type: 'uart',
    hardware_id: '0x01',
    config: {},
    status: 'active',
    last_data: null,
    last_data_time: null,
    created_at: '',
    ...overrides,
  }
}

const RouterLinkStub = defineComponent({
  props: { to: { type: [String, Object], required: true } },
  setup(props, { slots }) {
    return () => h('a', { class: 'router-link-stub', href: String(props.to) }, slots.default?.())
  },
})

function mountCard(device: EdgeDevice) {
  return mount(DeviceInfoCard, {
    props: { device },
    global: { stubs: { RouterLink: RouterLinkStub } },
  })
}

/**
 * 说明（2026-10-20 窄屏改版）：
 * 本文件的 useResponsive mock 返回 `isMobile: { value: false }`（普通对象，不是 computed），
 * 模板里 `v-if="isMobile"` 拿到的是**对象本身** ⇒ 恒为真 ⇒ 这些用例实际渲染的是**窄屏分支**
 * （mobile-info-list），不是桌面 el-descriptions。这一点在改版前不影响断言（两分支字段一致），
 * 但现在窄屏把「通信协议/硬件类型/硬件ID/所属节点/节点固件/配置版本」收进了「更多属性」折叠区，
 * 因此断言前必须先把折叠区展开 —— 否则测的是「折叠态」，不是「字段是否存在」。
 */
function expandMore(wrapper: ReturnType<typeof mountCard>) {
  const toggle = wrapper.find('.mobile-info-toggle')
  expect(toggle.exists(), '窄屏应渲染「更多属性」折叠入口').toBe(true)
  return toggle.trigger('click')
}

describe('DeviceInfoCard node link & versions (BMS 设计稿对齐)', () => {
  it('renders 所属节点 as a link to the node overview page (numeric PK)', async () => {
    const wrapper = mountCard(makeDevice({ node: { id: 7, name: 'node-A' } }))
    await expandMore(wrapper)
    const link = wrapper.find('a.router-link-stub')
    expect(link.exists()).toBe(true)
    expect(link.attributes('href')).toBe('/node/7/overview')
    expect(link.text()).toContain('node-A')
    expect(wrapper.text()).toContain('所属节点')
  })

  it('falls back to the physical node_id (hex) when the preloaded node lacks an id', async () => {
    const wrapper = mountCard(makeDevice({ node: { id: 'F0F5BDFFFE02', name: 'node-A' } }))
    await expandMore(wrapper)
    expect(wrapper.find('a.router-link-stub').attributes('href')).toBe('/node/F0F5BDFFFE02/overview')
  })

  it('links with raw node_id when the node preload is missing entirely', async () => {
    const wrapper = mountCard(makeDevice({ node: undefined }))
    await expandMore(wrapper)
    const link = wrapper.find('a.router-link-stub')
    expect(link.exists()).toBe(true)
    expect(link.attributes('href')).toBe('/node/F0F5BDFFFE02/overview')
  })

  it('renders 节点固件 and 配置版本 only when present', async () => {
    const withVersions = mountCard(makeDevice({
      node: { id: 7, name: 'node-A', firmware_version: '2.5.21' },
      config_version: 'v2-abc123',
    }))
    await expandMore(withVersions)
    expect(withVersions.text()).toContain('节点固件')
    expect(withVersions.text()).toContain('2.5.21')
    expect(withVersions.text()).toContain('配置版本')
    expect(withVersions.text()).toContain('v2-abc123')

    const without = mountCard(makeDevice())
    await expandMore(without)
    expect(without.text()).not.toContain('节点固件')
    expect(without.text()).not.toContain('配置版本')
  })

  it('窄屏默认收起次要属性（省 5 行），但入口文案给出可展开项数', () => {
    const wrapper = mountCard(makeDevice({
      node: { id: 7, name: 'node-A', firmware_version: '2.5.21' },
      config_version: 'v2-abc123',
    }))
    // 折叠态：次要字段**不在 DOM 里**（不是 display:none —— 那会残留读屏噪声）
    expect(wrapper.find('.mobile-info-toggle').attributes('aria-expanded')).toBe('false')
    expect(wrapper.text()).not.toContain('节点固件')
    expect(wrapper.text()).toContain('更多属性')
    // 反向护栏：关键四项必须在折叠态就可见，不能被一起收起来
    expect(wrapper.text()).toContain('设备名称')
    expect(wrapper.text()).toContain('设备类型')
    expect(wrapper.text()).toContain('健康状态')
    expect(wrapper.text()).toContain('最后数据时间')
  })
})
