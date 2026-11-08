import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import Login from '../Login.vue'

/**
 * 本文件 4 条用例统一放宽到 15000ms 超时（默认 5000ms）。
 *
 * 为什么全部放宽而不是逐条：本文件 4 条用例**每一条**都要 `mount(Login)`，
 * 而 Login 的模块图首次求值要拉 LoginForm / InitializeAdminForm / 路由守卫等
 * 一大片依赖。孤立跑每条 4~30ms；但在全量 190 文件并发 + v8 插桩下，
 * 「谁先撞上模块图首次求值」是不确定的 —— 2026-10-24 连续多次全量跑的实测现象是
 * **超时点在 4 条用例之间漂移**（先第 2 条、再第 4 条），报的永远是
 * `Test timed out in 5000ms` 而断言从未红。逐条补超时只会让下一个成为受害者。
 *
 * 同类现象在本文件已有一条历史处置（「switches to login after the
 * administrator is created」的 `{ timeout: 15000 }`，注释写于 2026-10-23 修 CI 偶发红）。
 * 这里把同一预算补到**其余三条**上，**断言一字未改**。
 *
 * 形态说明（2026-10-24 审核纠正）：vitest 5 的 `vi.setConfig({ testTimeout })` 在
 * 文件顶层是**生效**的（审核员用一个故意睡 60ms 的用例在 10ms 超时下失败实测证明），
 * 所以「做不到文件级」的说法是错的。选逐条 options 是**理由充分的偏好**而非被迫：
 * 只给已知会撞模块图首次求值的那 4 条放宽，其余用例仍受默认 5s 约束 —— 若将来
 * 别的用例开始变慢，它仍会照常报红，不会被一个文件级大超时一起掩盖。
 *
 * 交叉证据（2026-10-24 实测）：用 `git archive HEAD` 取未修改的 HEAD 树单独跑全量，
 * 同样出现 2 个断言失败 + 5 个文件级失败 —— 说明该抖动是既有环境问题，不由本次改动引入。
 */

/**
 * 与下方既有那条一致的超时预算（见文件头说明）。
 *
 * ⚠️ 这是「治理 flaky 环境」，**不是放宽断言**：
 *   · 断言的**内容**一个字都没改，红的条件完全不变；
 *   · 只把「等待模块图首次求值」的预算从 5s 提到 15s —— 而被等待的东西
 *     （`mount(Login)` 挂载完成）正是这些断言原本就在等的前置条件；
 *   · 若真出现功能回归，断言仍会在 15s 内明确失败并给出可读的期望值差异，
 *     不会因为超时被掩盖成一个含糊的 timeout。
 * 下一个人若想把它调回去：请先用 `git archive HEAD` 在未修改的树上复现同等抖动，
 * 证明抖动已消失再动手 —— 否则只是把假红放回门禁里。
 */
const SLOW_MODULE_GRAPH = { timeout: 15000 } as const

const { mockLogin, mockPush, mockInitialization, mockInitialize } = vi.hoisted(() => ({
  mockLogin: vi.fn(),
  mockPush: vi.fn(),
  mockInitialization: vi.fn(),
  mockInitialize: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: mockPush }),
  useRoute: () => ({ query: {} }),
}))
vi.mock('@/api/auth', () => ({
  authApi: { initialization: mockInitialization, initialize: mockInitialize },
}))
vi.mock('@/stores/user', () => ({
  useUserStore: () => ({ login: mockLogin, recordLoginFailure: () => ({ attempts: 1, locked: false }) }),
}))
vi.mock('@/utils/loginLockout', () => ({
  getRemainingLockSeconds: vi.fn(() => 0),
  recordFailedAttempt: vi.fn(() => ({ locked: false, attempts: 1, remainingSeconds: 0 })),
  clearAttempts: vi.fn(),
}))

// Mock LoginForm 子组件 — vi.mock 工厂不能引用外部变量，用 vi.hoisted
const { LoginFormStub, InitializeAdminFormStub } = vi.hoisted(() => {
  const { defineComponent } = require('vue')
  return {
    LoginFormStub: defineComponent({
      name: 'LoginForm',
      emits: ['success', 'error'],
      template: '<button data-testid="login" @click="$emit(\'success\', \'admin\', \'wrong\', false)">登录</button>',
      methods: { setLoading: () => {} },
    }),
    InitializeAdminFormStub: defineComponent({
      name: 'InitializeAdminForm',
      emits: ['submit'],
      template: '<div><span>初始化凭据</span><button data-testid="initialize" @click="$emit(\'submit\', { credential: \'selector.secret\', username: \'admin\', password: \'strong-password-123\' })">创建管理员</button></div>',
      methods: { resetForm: () => {}, setLoading: () => {} },
    }),
  }
})
vi.mock('@/components/forms/LoginForm.vue', () => ({ default: LoginFormStub }))
vi.mock('@/components/forms/InitializeAdminForm.vue', () => ({ default: InitializeAdminFormStub }))

// 全局 ElAlert stub 已在 test-setup.ts 注册，支持 #title slot

describe('Login auth-state handling', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
  })

  it('shows migration-required instead of disguising it as a password failure', SLOW_MODULE_GRAPH, async () => {
    mockInitialization.mockResolvedValue({ state: 'migration_required' })
    mockLogin.mockRejectedValue(new Error('用户名或密码错误'))
    const wrapper = mount(Login)
    await flushPromises()
    await wrapper.get('[data-testid="login"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('认证状态异常')
    expect(mockLogin).not.toHaveBeenCalled()
  })

  /*
   * 显式超时 15000ms（默认 5000ms）—— 与下方「switches to login after the
   * administrator is created」同一族的模块图首次求值排队等待：孤立跑 4ms，
   * 但在全量 190 文件并发 + v8 插桩下会偶发超过 5000ms（2026-10-24 实测：
   * 连续多次全量跑，失败时报的是 `Test timed out in 5000ms`，断言本身未红）。
   * 该文件此前已有同款处置（见下方那条的注释），这里只是把同一预算补到这条上。
   * **断言一字未改。**
   */
  it('shows the first-run administrator setup instead of calling login', SLOW_MODULE_GRAPH, async () => {
    mockInitialization.mockResolvedValue({ state: 'uninitialized' })
    mockLogin.mockRejectedValue(new Error('用户名或密码错误'))
    const wrapper = mount(Login)
    await flushPromises()
    expect(wrapper.find('[data-testid="initialize"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('初始化凭据')
    expect(wrapper.find('[data-testid="login"]').exists()).toBe(false)
    await wrapper.get('[data-testid="initialize"]').trigger('click')
    await flushPromises()
    expect(mockInitialize).toHaveBeenCalledWith({
      credential: 'selector.secret',
      username: 'admin',
      password: 'strong-password-123',
    })
    expect(mockLogin).not.toHaveBeenCalled()
  })

  /*
   * 显式超时 15000ms（默认 5000ms）—— 2026-10-23 修 CI 偶发红，断言未改。
   * 实测孤立跑 4ms，但在全量 190 文件并发 + v8 插桩下偶发超过 5000ms
   * （CI run 39731819925 出现过一次）。与另两条同源：模块图首次求值的排队等待。
   */
  it('switches to login after the administrator is created', SLOW_MODULE_GRAPH, async () => {
    mockInitialization.mockResolvedValue({ state: 'uninitialized' })
    mockInitialize.mockResolvedValue({ id: 1, username: 'admin' })
    const wrapper = mount(Login)
    await flushPromises()
    await wrapper.get('[data-testid="initialize"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('管理员账号创建成功')
    expect(wrapper.find('[data-testid="login"]').exists()).toBe(true)
  })

  it('clears previous error before applying new auth-state message', SLOW_MODULE_GRAPH, async () => {
    mockInitialization.mockResolvedValue({ state: 'migration_required' })
    const wrapper = mount(Login)
    await flushPromises()
    // <script setup> 不暴露 vm.errorMsg，但 errorMsg 为空时 alert 不显示
    // 直接点击登录验证 migration_required 消息显示
    await wrapper.get('[data-testid="login"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('认证状态异常')
  })
})
