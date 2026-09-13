import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import Forbidden from '../Forbidden.vue'

// Mock vue-router
const mockPush = vi.fn()
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: mockPush }),
}))

// Mock user store：userInfo 可变，用于覆盖「有身份 / 无身份 / 无 role」三种情况。
// 注意：stores/user.ts 的 UserInfo 里根本没有 role 字段，页面不得凭空渲染角色名。
const { userInfoRef } = vi.hoisted(() => ({ userInfoRef: { value: null as unknown } }))
vi.mock('@/stores/user', () => ({
  useUserStore: () => ({
    get userInfo() {
      return userInfoRef.value
    },
    logout: vi.fn(() => Promise.resolve()),
  }),
}))

// Mock icons
vi.mock('@element-plus/icons-vue', () => ({
  HomeFilled: { name: 'HomeFilled', template: '<i />' },
  SwitchButton: { name: 'SwitchButton', template: '<i />' },
}))

// 全局 Element Plus stub 已在 src/test-setup.ts 注册。
// ErrorPageLayout 是项目内组件，需要 stub 以隔离测试。
const stubs = {
  ErrorPageLayout: {
    props: ['code', 'title', 'gradient', 'maxWidth'],
    template: `<div class="error-page">
      <div class="error-code">{{ code }}</div>
      <div class="error-title">{{ title }}</div>
      <div class="error-description"><slot name="description" /></div>
      <div class="error-actions"><slot name="actions" /></div>
    </div>`,
  },
}

describe('Forbidden.vue', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
    userInfoRef.value = null
  })

  it('renders the forbidden error page', async () => {
    const wrapper = mount(Forbidden, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.error-page').exists()).toBe(true)
  })

  it('displays 403 error code', async () => {
    const wrapper = mount(Forbidden, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.error-code').text()).toBe('403')
  })

  it('displays "无权访问" title', async () => {
    const wrapper = mount(Forbidden, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.error-title').text()).toBe('无权访问')
  })

  it('computes username from store', async () => {
    userInfoRef.value = { id: 2, username: 'viewer1' }
    const wrapper = mount(Forbidden, { global: { stubs } })
    await flushPromises()
    const description = wrapper.find('.error-description').text()
    expect(description).toContain('viewer1')
    // 账号名与权限结论之间不再夹带任何角色名
    expect(description).not.toContain('（')
  })

  /**
   * 回归（规范 §3.2.5「接口缺字段时显示未知，不得以本地默认值伪造」）：
   * 旧实现硬编码 <el-tag>系统管理员</el-tag>，未登录直链 /403 时会输出
   * 「当前账号 当前用户（系统管理员）」——用户身份是编造的。
   * UserInfo 类型没有 role 字段，所以任何情况下都不得渲染角色名。
   */
  it('未登录（userInfo 为 null）时不出现角色名「系统管理员」', async () => {
    userInfoRef.value = null
    const wrapper = mount(Forbidden, { global: { stubs } })
    await flushPromises()
    const text = wrapper.text()
    expect(text).toContain('当前用户')
    expect(text).not.toContain('系统管理员')
    expect(wrapper.find('.el-tag').exists()).toBe(false)
  })

  it('已登录但 store 无 role 字段时不出现角色名「系统管理员」', async () => {
    userInfoRef.value = { id: 2, username: 'viewer1', email: 'viewer1@example.com', enabled: true }
    const wrapper = mount(Forbidden, { global: { stubs } })
    await flushPromises()
    const text = wrapper.text()
    expect(text).toContain('viewer1')
    expect(text).not.toContain('系统管理员')
    expect(wrapper.find('.el-tag').exists()).toBe(false)
    // 身份句只陈述账号名，不附加任何角色归属
    const identitySentence = wrapper.findAll('.error-description p')[0].text()
    expect(identitySentence).toContain('viewer1')
    expect(identitySentence).not.toContain('管理员')
    expect(identitySentence).not.toContain('（')
  })

  it('提示文案不把「联系管理员」写成具体角色', async () => {
    userInfoRef.value = { id: 2, username: 'viewer1' }
    const wrapper = mount(Forbidden, { global: { stubs } })
    await flushPromises()
    // 「请联系管理员调整角色权限」是通用指代，不得是「系统管理员」这类被伪造的具体角色
    expect(wrapper.text()).toContain('请联系管理员调整角色权限')
  })

  it('goHome navigates to dashboard', async () => {
    const wrapper = mount(Forbidden, { global: { stubs } })
    await flushPromises()
    // 通过 DOM 交互触发 goHome，而非 wrapper.vm.goHome()
    // "返回首页" 按钮绑定了 @click="goHome"
    const buttons = wrapper.findAll('.el-button')
    const homeBtn = buttons.find(b => b.text().includes('返回首页'))
    expect(homeBtn).toBeTruthy()
    await homeBtn!.trigger('click')
    expect(mockPush).toHaveBeenCalledWith('/dashboard')
  })
})
