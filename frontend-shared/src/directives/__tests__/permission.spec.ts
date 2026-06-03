import { describe, it, expect, beforeEach } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { defineComponent, h } from 'vue'
import permission from '../permission'
import { useUserStore } from '@/stores/user'

describe('v-permission 指令', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('value 为空/undefined 时不隐藏 (向后兼容)', () => {
    const store = useUserStore()
    store.userInfo = { id: 1, username: 't', email: 't@x.com', role: 'viewer' }
    const Comp = defineComponent({
      setup() {
        return () => h('button', { 'data-testid': 'btn', 'v-permission': '' }, 'x')
      },
    })
    const wrapper = mount(Comp, {
      attrs: { 'v-permission': undefined } as any,
      global: { directives: { permission } },
    })
    expect(wrapper.find('[data-testid="btn"]').exists()).toBe(true)
  })

  it('viewer 角色看到 admin 专属元素被隐藏', async () => {
    const store = useUserStore()
    store.userInfo = { id: 1, username: 't', email: 't@x.com', role: 'viewer' }
    const Comp = defineComponent({
      template: `<div v-permission="'admin'" data-testid="el">content</div>`,
    })
    const wrapper = mount(Comp, { global: { directives: { permission } } })
    const el = wrapper.find('[data-testid="el"]').element as HTMLElement
    expect(el.style.display).toBe('none')
  })

  it('admin 角色看到 admin 专属元素正常显示', () => {
    const store = useUserStore()
    store.userInfo = { id: 1, username: 't', email: 't@x.com', role: 'admin' }
    const Comp = defineComponent({
      template: `<div v-permission="'admin'" data-testid="el">content</div>`,
    })
    const wrapper = mount(Comp, { global: { directives: { permission } } })
    const el = wrapper.find('[data-testid="el"]').element as HTMLElement
    expect(el.style.display).not.toBe('none')
  })

  it('数组形式任一匹配即可 (默认 :any)', () => {
    const store = useUserStore()
    store.userInfo = { id: 1, username: 't', email: 't@x.com', role: 'operator' }
    const Comp = defineComponent({
      template: `<div v-permission="['admin', 'operator']" data-testid="el">content</div>`,
    })
    const wrapper = mount(Comp, { global: { directives: { permission } } })
    const el = wrapper.find('[data-testid="el"]').element as HTMLElement
    expect(el.style.display).not.toBe('none')
  })

  it('未登录用户所有受控元素都被隐藏', () => {
    const store = useUserStore()
    store.userInfo = null
    const Comp = defineComponent({
      template: `<div v-permission="'admin'" data-testid="el">content</div>`,
    })
    const wrapper = mount(Comp, { global: { directives: { permission } } })
    const el = wrapper.find('[data-testid="el"]').element as HTMLElement
    expect(el.style.display).toBe('none')
  })

  it('updated 钩子: 角色变化时重新评估', async () => {
    const store = useUserStore()
    store.userInfo = { id: 1, username: 't', email: 't@x.com', role: 'viewer' }
    const Comp = defineComponent({
      template: `<div v-permission="'admin'" data-testid="el">content</div>`,
    })
    const wrapper = mount(Comp, { global: { directives: { permission } } })
    expect((wrapper.find('[data-testid="el"]').element as HTMLElement).style.display).toBe('none')

    // 模拟角色提升
    store.userInfo.role = 'admin'
    await wrapper.vm.$nextTick()
    expect((wrapper.find('[data-testid="el"]').element as HTMLElement).style.display).not.toBe('none')
  })
})
