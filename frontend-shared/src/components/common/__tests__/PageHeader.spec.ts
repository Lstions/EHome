import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import PageHeader from '@/components/common/PageHeader.vue'

describe('PageHeader.vue', () => {
  it('renders subtitle and actions from the canonical extra slot', () => {
    const wrapper = mount(PageHeader, {
      props: { title: '通道管理', subtitle: '查看和管理所有节点的通道配置' },
      slots: { extra: '<button>刷新</button>' },
      global: {
        stubs: {
          'el-button': { template: '<button><slot /></button>' },
        },
      },
    })

    expect(wrapper.text()).toContain('通道管理')
    expect(wrapper.text()).toContain('查看和管理所有节点的通道配置')
    expect(wrapper.find('button').text()).toBe('刷新')
  })
})