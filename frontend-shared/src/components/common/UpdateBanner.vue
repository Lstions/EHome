<template>
  <div class="update-banner-root">
    <!-- 横幅：仅 stale 且未被「断线」压住时出现（断线优先，§6.4 UX 规则 2） -->
    <transition name="app-update-slide">
      <div
        v-if="showBanner"
        class="update-banner"
        role="status"
        aria-live="polite"
        data-testid="update-banner"
      >
        <el-icon class="banner-icon"><RefreshRight /></el-icon>
        <div class="banner-content">
          <strong class="banner-title">系统已发布新版本</strong>
          <span class="banner-msg">{{ bannerMessage }}</span>
        </div>
        <div class="banner-actions">
          <el-button
            v-if="store.hasReleaseNotes"
            link
            type="primary"
            data-testid="update-notes-link"
            @click="store.openReleaseNotes()"
          >查看更新内容</el-button>
          <el-button link data-testid="update-later" @click="store.dismiss()">稍后</el-button>
          <el-button type="primary" size="small" data-testid="update-refresh" @click="store.refreshNow()">
            立即刷新
          </el-button>
        </div>
      </div>
    </transition>

    <!-- 模态：chunk 加载失败（含 stale 期间导航失败）—— **不可推迟**、不可关闭。
         只给「立即刷新」一条出路：此时页面已无法正常导航，继续使用只会不断撞 404。 -->
    <el-dialog
      v-if="store.modalVisible"
      :model-value="true"
      title="需要刷新后才能继续"
      width="440px"
      align-center
      :close-on-click-modal="false"
      :close-on-press-escape="false"
      :show-close="false"
      data-testid="update-blocked-modal"
      class="update-blocked-dialog"
    >
      <div class="blocked-body" data-testid="update-blocked-body">
        <p>新版本已发布，当前页面引用的资源已不存在，继续操作会不断失败。</p>
        <p>请立即刷新页面（未提交的输入会丢失）。</p>
      </div>
      <template #footer>
        <el-button
          v-if="store.hasReleaseNotes"
          link
          type="primary"
          data-testid="update-blocked-notes-link"
          @click="store.openReleaseNotes()"
        >查看更新内容</el-button>
        <el-button type="primary" data-testid="update-blocked-refresh" @click="store.refreshNow()">
          立即刷新
        </el-button>
      </template>
    </el-dialog>

    <!-- 两个入口共用这一个实例（横幅 / 「关于」都只改 store.releaseNotesVisible） -->
    <ReleaseNotesDialog />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { RefreshRight } from '@element-plus/icons-vue'
import ReleaseNotesDialog from '@/components/common/ReleaseNotesDialog.vue'
import { useAppUpdateStore } from '@/stores/appUpdate'

/**
 * 旧页面更新提示（契约 §6.4 / T2.6：三种呈现 横幅 / 模态 / 无）。
 *
 * 与 NetworkBanner 的关系（**共用顶部栈，断线优先**）：
 * - NetworkBanner 是 `position: fixed; top: 0; z-index: 9999`；
 * - 本横幅是 `top: 0; z-index: 9998`，且父级通过 `defer` 在断线时整块收起 ——
 *   两个 fixed top **不会重叠**，也不会出现「刷新提示盖住『网络已断开』」。
 *   选择「延后」而不是「下移」：下移需要测量对方高度，任何一个横幅改字号就会错位；
 *   而断线时刷新本来也无意义（刷新后照样连不上），延后语义更正确。
 *
 * 三种呈现：
 * | state     | 呈现 | 可推迟 |
 * |-----------|------|--------|
 * | stale     | 横幅 | 是（「稍后」） |
 * | blocked   | 模态 | 否 |
 * | same/dismissed | 无 | — |
 */
const props = withDefaults(defineProps<{
  /** 断线横幅正在显示 ⇒ 更新提示延后（不渲染横幅；模态不受影响）。 */
  defer?: boolean
}>(), {
  defer: false,
})

const store = useAppUpdateStore()

/**
 * 当前页面的身份：有版本号就显示版本号；**没有版本号就显示 build id**。
 *
 * 为什么不能回退成「未知」：`VITE_APP_VERSION` 自 M2 起不再注入（版本唯一真源是后端
 * `/system/version`），若这里回退「未知」，横幅会变成「当前页面为 v未知」——
 * 既没信息量，又像是缺陷。build id 在能触发本横幅的环境里必然存在（生产构建注入），
 * 拿它做身份既准确又可对账；两者皆无时整段省略，绝不输出占位符。
 */
const currentIdentity = computed(() => {
  const version = (import.meta.env.VITE_APP_VERSION || '').trim()
  if (version) return `v${version}`
  const buildId = store.localBuildId
  return buildId ? `构建 ${buildId}` : ''
})

/** 服务端身份：版本号优先，其次 build id。 */
const serverIdentity = computed(() => {
  if (store.serverVersion) return `v${store.serverVersion}`
  return store.serverBuildId ? `构建 ${store.serverBuildId}` : ''
})

const bannerMessage = computed(() => {
  const parts: string[] = []
  if (currentIdentity.value) parts.push(`当前页面为 ${currentIdentity.value}`)
  if (serverIdentity.value) parts.push(`服务端已是 ${serverIdentity.value}`)
  const prefix = parts.length ? `${parts.join('，')}；` : ''
  return `${prefix}刷新后继续使用（不会自动刷新）。`
})

const showBanner = computed(() => store.bannerVisible && !props.defer)
</script>

<style scoped>
/* z-index 必须**小于** NetworkBanner(9999)：同为 top:0 时断线横幅在上层。 */
.update-banner {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  z-index: 9998;
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 24px;
  font-size: 14px;
  /* 与 NetworkBanner 同款「浅底 + 基色文字」：暗色主题下实心渐变会塌到 2:1 以下 */
  background: var(--el-color-primary-light-9);
  color: var(--el-color-primary);
  border-bottom: 1px solid var(--el-color-primary-light-7);
  box-shadow: var(--shadow-md);
}
.banner-icon {
  font-size: 20px;
  flex: 0 0 auto;
}
.banner-content {
  flex: 1;
  display: flex;
  align-items: baseline;
  gap: 8px;
  flex-wrap: wrap;
  min-width: 0;
}
.banner-title {
  font-weight: 600;
}
.banner-msg {
  font-size: 13px;
  opacity: 0.9;
}
.banner-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 0 0 auto;
}
.blocked-body p {
  margin: 0 0 8px;
  font-size: 14px;
  line-height: 1.6;
  color: var(--el-text-color-regular);
}
.app-update-slide-enter-active,
.app-update-slide-leave-active {
  transition: transform 0.3s ease, opacity 0.3s ease;
}
.app-update-slide-enter-from,
.app-update-slide-leave-to {
  transform: translateY(-100%);
  opacity: 0;
}
@media (max-width: 768px) {
  /* 窄屏：说明与操作分两行，按钮不被挤出视口（390px 下实测过） */
  .update-banner {
    flex-wrap: wrap;
    padding: 10px 16px;
  }
  .banner-content {
    flex: 1 1 calc(100% - 32px);
  }
  .banner-actions {
    width: 100%;
    justify-content: flex-end;
  }
}
@media (prefers-reduced-motion: reduce) {
  .app-update-slide-enter-active,
  .app-update-slide-leave-active {
    transition: none;
  }
}
</style>
