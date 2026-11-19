import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import UpdateBanner from '@/components/common/UpdateBanner.vue'
import ReleaseNotesDialog from '@/components/common/ReleaseNotesDialog.vue'
import { resetAppUpdateBridgeForTests, useAppUpdateStore } from '@/stores/appUpdate'

/**
 * 更新横幅 / 模态 / 「关于与更新内容」弹窗（契约 §6.4 / AC-16 / AC-18）。
 *
 * 三种呈现必须逐一钉住：多一种（比如 dismissed 还出横幅）就是打扰，
 * 少一种（blocked 不出模态）就是让用户撞 404。notes 为空时**不许**出现入口 ——
 * 那会变成「只有版本号的空弹窗」，正是方案 v1.5 要消灭的形态。
 */

function setupStore() {
  setActivePinia(createPinia())
  return useAppUpdateStore()
}

/** 直接把 store 推到某状态（不经过网络，组件只关心渲染契约）。 */
function pushServerInfo(store: ReturnType<typeof setupStore>, patch: Partial<{
  version: string
  build_id: string
  build_time: string
  notes: string[]
}> = {}) {
  store.applyVersionInfo({
    version: '2.9.0',
    build_id: 'build-server-bbb',
    commit: 'abc',
    build_time: '2099-02-01T00:00:00Z',
    notes: ['固件上传改为必填 sidecar', '闸门新增节点硬件档案校验'],
    ...patch,
  })
}

/** 把 window.location 换成一个只含 reload 的桩（happy-dom 的 reload 不可 spy）。 */
function stubLocation() {
  const original = window.location
  const reload = vi.fn()
  delete (window as unknown as { location?: Location }).location
  ;(window as unknown as Record<string, unknown>).location = { ...original, reload }
  return {
    reload,
    restore: () => {
      ;(window as unknown as Record<string, unknown>).location = original
    },
  }
}

let stopWs: (() => void) | null = null

beforeEach(() => {
  vi.clearAllMocks()
  resetAppUpdateBridgeForTests()
  sessionStorage.clear()
  vi.stubEnv('VITE_BUILD_ID', 'build-local-aaa')
  vi.stubEnv('VITE_BUILD_TIME', '2026-01-01T00:00:00Z')
  vi.stubEnv('VITE_APP_VERSION', '')
})

afterEach(() => {
  vi.unstubAllEnvs()
  resetAppUpdateBridgeForTests()
  if (stopWs) {
    stopWs()
    stopWs = null
  }
})

describe('UpdateBanner · 三种呈现', () => {
  it('stale → 横幅 + 三个按钮（立即刷新 / 稍后 / 查看更新内容）', async () => {
    const store = setupStore()
    pushServerInfo(store)
    const wrapper = mount(UpdateBanner)
    await flushPromises()

    expect(store.state).toBe('stale')
    const banner = wrapper.find('.update-banner')
    expect(banner.exists()).toBe(true)
    expect(banner.attributes('role')).toBe('status')
    const text = wrapper.text()
    expect(text).toContain('系统已发布新版本')
    expect(wrapper.find('[data-testid="update-refresh"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="update-later"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="update-notes-link"]').exists()).toBe(true)
    // stale 不是模态：不得同时出现阻断弹窗
    expect(wrapper.find('.el-dialog').exists()).toBe(false)
    wrapper.unmount()
  })

  it('dismissed → 无任何呈现（不主动打扰）', async () => {
    const store = setupStore()
    pushServerInfo(store)
    store.dismiss()
    const wrapper = mount(UpdateBanner)
    await flushPromises()
    expect(wrapper.find('.update-banner').exists()).toBe(false)
    expect(wrapper.find('.el-dialog').exists()).toBe(false)
    wrapper.unmount()
  })

  it('same → 无任何呈现', async () => {
    const store = setupStore()
    pushServerInfo(store, { build_id: 'build-local-aaa' })
    expect(store.state).toBe('same')
    const wrapper = mount(UpdateBanner)
    await flushPromises()
    expect(wrapper.find('.update-banner').exists()).toBe(false)
    wrapper.unmount()
  })

  it('blocked → 模态（不可推迟），横幅让位', async () => {
    const store = setupStore()
    pushServerInfo(store)
    // 组件层只关心「进入 blocked 之后怎么渲染」；桥接入口在 store spec 里单独覆盖
    store.markBlocked('Failed to fetch dynamically imported module')
    const wrapper = mount(UpdateBanner)
    await flushPromises()

    expect(store.state).toBe('blocked')
    expect(wrapper.find('.update-banner').exists()).toBe(false)
    const dialog = wrapper.find('[data-testid="update-blocked-body"]')
    expect(dialog.exists()).toBe(true)
    expect(wrapper.text()).toContain('需要刷新后才能继续')
    // 模态上只有「立即刷新」（+ 有 notes 时的查看内容），没有「稍后」
    expect(wrapper.find('[data-testid="update-blocked-refresh"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="update-later"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('断线优先：defer=true 时横幅延后（不渲染），模态不受影响', async () => {
    const store = setupStore()
    pushServerInfo(store)
    const wrapper = mount(UpdateBanner, { props: { defer: true } })
    await flushPromises()
    expect(store.state).toBe('stale')
    expect(wrapper.find('.update-banner').exists()).toBe(false)

    // 模态本身是阻断式提示，断线也要显示（否则用户卡在失效页面）
    store.markBlocked('module failed')
    await flushPromises()
    expect(wrapper.find('[data-testid="update-blocked-body"]').exists()).toBe(true)
    wrapper.unmount()
  })
})

describe('UpdateBanner · 交互', () => {
  it('点「稍后」→ dismissed 并把 build 记进 sessionStorage', async () => {
    const store = setupStore()
    pushServerInfo(store)
    const wrapper = mount(UpdateBanner)
    await flushPromises()

    await wrapper.find('[data-testid="update-later"]').trigger('click')
    expect(store.state).toBe('dismissed')
    expect(sessionStorage.getItem('ehome.app-update.dismissed-build')).toBe('build-server-bbb')
    expect(wrapper.find('.update-banner').exists()).toBe(false)
    wrapper.unmount()
  })

  it('点「立即刷新」→ 只调用 location.reload 一次（绝不自动刷新）', async () => {
    const store = setupStore()
    pushServerInfo(store)
    const loc = stubLocation()
    const wrapper = mount(UpdateBanner)
    await flushPromises()

    expect(loc.reload).not.toHaveBeenCalled()
    await wrapper.find('[data-testid="update-refresh"]').trigger('click')
    expect(loc.reload).toHaveBeenCalledTimes(1)
    loc.restore()
    wrapper.unmount()
  })

  it('点「查看更新内容」→ 打开弹窗并列出全部 notes，可再「立即刷新」', async () => {
    const store = setupStore()
    pushServerInfo(store)
    const wrapper = mount(UpdateBanner)
    await flushPromises()

    await wrapper.find('[data-testid="update-notes-link"]').trigger('click')
    await flushPromises()
    expect(store.releaseNotesVisible).toBe(true)

    const body = wrapper.find('[data-testid="release-notes-body"]')
    expect(body.exists()).toBe(true)
    expect(body.text()).toContain('固件上传改为必填 sidecar')
    expect(body.text()).toContain('闸门新增节点硬件档案校验')
    expect(wrapper.find('[data-testid="release-notes-refresh"]').exists()).toBe(true)
    wrapper.unmount()
  })
})

describe('ReleaseNotesDialog', () => {
  it('notes 为空 → 整个弹窗不渲染（不许出现只有版本号的空弹窗）', async () => {
    const store = setupStore()
    pushServerInfo(store, { notes: [] })
    store.releaseNotesVisible = true
    const wrapper = mount(ReleaseNotesDialog)
    await flushPromises()
    expect(wrapper.find('[data-testid="release-notes-body"]').exists()).toBe(false)
    expect(wrapper.text()).toBe('')
    wrapper.unmount()
  })

  it('notes 有内容 → 逐条展示，且与 store 完全一致（不截断）', async () => {
    const store = setupStore()
    const notes = Array.from({ length: 10 }, (_, i) => '变更 #' + (i + 1))
    pushServerInfo(store, { notes })
    store.releaseNotesVisible = true
    const wrapper = mount(ReleaseNotesDialog)
    await flushPromises()

    const items = wrapper.findAll('[data-testid="release-note-item"]')
    expect(items).toHaveLength(notes.length)
    items.forEach((item, index) => {
      expect(item.text()).toBe(notes[index])
    })
    wrapper.unmount()
  })

  it('非 stale/blocked 时只有「关闭」，不出现「立即刷新」', async () => {
    const store = setupStore()
    pushServerInfo(store, { build_id: 'build-local-aaa' })
    expect(store.state).toBe('same')
    store.releaseNotesVisible = true
    const wrapper = mount(ReleaseNotesDialog)
    await flushPromises()
    expect(wrapper.find('[data-testid="release-notes-close"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="release-notes-refresh"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('blocked 时弹窗可关，但模态仍在（关弹窗不改变更新状态）', async () => {
    const store = setupStore()
    pushServerInfo(store)
    store.markBlocked('module failed')
    const wrapper = mount(UpdateBanner)
    await flushPromises()

    store.openReleaseNotes()
    await flushPromises()
    expect(wrapper.find('[data-testid="release-notes-body"]').exists()).toBe(true)
    store.closeReleaseNotes()
    await flushPromises()
    expect(store.state).toBe('blocked')
    expect(wrapper.find('[data-testid="update-blocked-body"]').exists()).toBe(true)
    wrapper.unmount()
  })
})

describe('UpdateBanner · 身份文案（不得出现「未知」）', () => {
  it('无 VITE_APP_VERSION → 展示 build id；任何位置都不出现「未知」', async () => {
    const store = setupStore()
    pushServerInfo(store)
    const wrapper = mount(UpdateBanner)
    await flushPromises()

    const text = wrapper.find('.banner-msg').text()
    expect(text).toContain('当前页面为 构建 build-local-aaa')
    expect(text).toContain('服务端已是 v2.9.0')
    expect(text).not.toContain('未知')
    expect(wrapper.text()).not.toContain('v未知')
    wrapper.unmount()
  })

  it('显式注入 VITE_APP_VERSION → 展示版本号（横幅不再出现 build id）', async () => {
    vi.stubEnv('VITE_APP_VERSION', '2.9.1')
    const store = setupStore()
    pushServerInfo(store)
    const wrapper = mount(UpdateBanner)
    await flushPromises()

    const text = wrapper.find('.banner-msg').text()
    expect(text).toContain('当前页面为 v2.9.1')
    expect(text).not.toContain('未知')
    // build id 仍可在「关于」弹窗里查到，但横幅只讲用户能理解的身份
    expect(text).not.toContain('build-local-aaa')
    wrapper.unmount()
  })

  it('服务端只有 build id（无 version）→ 用 build id 表述，仍不出现「未知」', async () => {
    const store = setupStore()
    store.applyVersionInfo({
      version: '',
      build_id: 'build-server-bbb',
      commit: '',
      build_time: '2099-02-01T00:00:00Z',
      notes: ['某条变更'],
    })
    expect(store.state).toBe('stale')
    const wrapper = mount(UpdateBanner)
    await flushPromises()

    const text = wrapper.find('.banner-msg').text()
    expect(text).toContain('服务端已是 构建 build-server-bbb')
    expect(text).not.toContain('未知')
    wrapper.unmount()
  })
})
