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

describe('DeviceInfoCard node link & versions (BMS 设计稿对齐)', () => {
  it('renders 所属节点 as a link to the node overview page (numeric PK)', () => {
    const wrapper = mountCard(makeDevice({ node: { id: 7, name: 'node-A' } }))
    const link = wrapper.find('a.router-link-stub')
    expect(link.exists()).toBe(true)
    expect(link.attributes('href')).toBe('/node/7/overview')
    expect(link.text()).toContain('node-A')
    expect(wrapper.text()).toContain('所属节点')
  })

  it('falls back to the physical node_id (hex) when the preloaded node lacks an id', () => {
    const wrapper = mountCard(makeDevice({ node: { id: 'F0F5BDFFFE02', name: 'node-A' } }))
    expect(wrapper.find('a.router-link-stub').attributes('href')).toBe('/node/F0F5BDFFFE02/overview')
  })

  it('links with raw node_id when the node preload is missing entirely', () => {
    const wrapper = mountCard(makeDevice({ node: undefined }))
    const link = wrapper.find('a.router-link-stub')
    expect(link.exists()).toBe(true)
    expect(link.attributes('href')).toBe('/node/F0F5BDFFFE02/overview')
  })

  it('renders 节点固件 and 配置版本 only when present', () => {
    const withVersions = mountCard(makeDevice({
      node: { id: 7, name: 'node-A', firmware_version: '2.5.21' },
      config_version: 'v2-abc123',
    }))
    expect(withVersions.text()).toContain('节点固件')
    expect(withVersions.text()).toContain('2.5.21')
    expect(withVersions.text()).toContain('配置版本')
    expect(withVersions.text()).toContain('v2-abc123')

    const without = mountCard(makeDevice())
    expect(without.text()).not.toContain('节点固件')
    expect(without.text()).not.toContain('配置版本')
  })
})
