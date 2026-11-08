<template>
  <el-config-provider :locale="locale">
    <!-- 启动期门禁（契约 §4）：ready 之前**不渲染 router-view**。
         为什么放在最外层而不是路由内：维护期的后端除白名单外一律 503，
         一旦进入 router-view，路由守卫会把未登录用户弹去 /login、并触发一串业务 chunk 加载 ——
         用户看到的会是登录页而不是「系统升级中」。放在这里就没有任何路由参与。 -->
    <Maintenance v-if="phase === 'maintenance'" :snapshot="snapshot" :connection-failed="connectionFailed" />
    <!-- 首次探测的宽限期（正常启动几十毫秒，上限 800ms）：既不渲染维护页（否则每次正常
         打开都闪一帧「系统升级中」），也不渲染业务内容（否则会先亮出半截应用再被盖掉）。
         **必须给一个可见的骨架**：空白 800ms 与闪一帧同样是体验缺陷，故这里渲染
         与维护页同构的轻量骨架（品牌 + 转圈 + 一句"正在检查系统状态"）。 -->
    <div v-else-if="phase === 'probing'" class="startup-gate-pending" role="status" aria-live="polite">
      <div class="startup-gate-pending__brand">
        <img src="/favicon.svg" alt="" class="startup-gate-pending__logo" />
        <span class="startup-gate-pending__name">EHomeSystem</span>
      </div>
      <el-icon class="startup-gate-pending__spinner"><Loading /></el-icon>
      <p class="startup-gate-pending__text">正在检查系统状态…</p>
    </div>
    <!-- ready 之后：与改造前**完全等价**的结构
         （ErrorBoundary > NetworkBanner > RouteProgressBar > router-view），
         既有测试与样式依赖都不得改变。 -->
    <ErrorBoundary v-else>
      <NetworkBanner />
      <!-- 全局路由进度条：懒加载 chunk 下载期间保持可见反馈。ErrorBoundary 之前，确保路由级错误也能被覆盖 -->
      <RouteProgressBar />
      <router-view />
    </ErrorBoundary>
  </el-config-provider>
</template>

<script setup lang="ts">
import { watch } from 'vue'
import { Loading } from '@element-plus/icons-vue'
import { useThemeStore } from '@/stores/theme'
import zhCn from 'element-plus/es/locale/lang/zh-cn'
import ErrorBoundary from '@/components/common/ErrorBoundary.vue'
import NetworkBanner from '@/components/common/NetworkBanner.vue'
import RouteProgressBar from '@/components/common/RouteProgressBar.vue'
import Maintenance from '@/views/maintenance/Maintenance.vue'
import { useStartupGate } from '@/views/maintenance/useStartupGate'

const themeStore = useThemeStore()

const locale = zhCn

/**
 * 启动期门禁：onMounted 探一次 `/api/v1/system/maintenance`，
 * 未就绪 → 渲染维护页并退避轮询；就绪 → 自动整页重载回正常入口。
 * 完整语义（含「后端不认识该接口则 fail-open」等边界）见 useStartupGate.ts 的文件头。
 */
const { phase, snapshot, connectionFailed } = useStartupGate()

// 监听主题变化，更新 document 类名
watch(() => themeStore.mode, (mode) => {
  if (mode === 'dark') {
    document.documentElement.classList.add('dark')
  } else {
    document.documentElement.classList.remove('dark')
  }
}, { immediate: true })
</script>

<style>
/* 全局样式 */
* {
  margin: 0;
  padding: 0;
  box-sizing: border-box;
}

html, body {
  width: 100%;
  height: 100%;
  overflow: hidden;
}

#app {
  width: 100%;
  height: 100%;
}

/* overlay可见时启用flex居中；隐藏时Element Plus内联display:none优先级更高，不受影响 */
.el-overlay {
  display: flex;
  align-items: center;
  justify-content: center;
}

/* 移动端输入防缩放：iOS Safari 对 font-size < 16px 的输入框聚焦时自动放大页面。
   全局 <style> 中 :deep() 无效，必须用普通选择器。 */
@media (max-width: 768px) {
  .el-input__inner,
  .el-textarea__inner,
  .el-select__wrapper {
    font-size: 16px;
  }
}

/* 移动端 Element Plus 组件字体缩放 */
@media (max-width: 480px) {
  .el-input__inner,
  .el-select .el-input__inner,
  .el-textarea__inner {
    font-size: 16px;
  }
  .el-button {
    font-size: 15px;
  }
  .el-form-item__label {
    font-size: 15px;
  }
  .el-table {
    font-size: 15px;
  }
  .el-card {
    font-size: 15px;
  }
  .el-tag {
    font-size: 14px;
  }
  .el-pagination {
    font-size: 14px;
  }
  .el-menu {
    font-size: 16px;
  }
  .el-tabs__item {
    font-size: 15px;
  }
  .el-dropdown-menu__item {
    font-size: 15px;
  }
}

/* 启动期骨架（首次探测宽限期内，最长 800ms）：与维护页同构，
   避免"空白 800ms"这另一种体验缺陷。 */
.startup-gate-pending {
  position: relative;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 16px;
  min-height: 100vh;
  min-height: 100dvh;
  background: var(--bg-color-page);
}
.startup-gate-pending__brand {
  position: absolute;
  top: 24px;
  left: 24px;
  display: flex;
  align-items: center;
  gap: 8px;
}
.startup-gate-pending__logo {
  width: 28px;
  height: 28px;
}
.startup-gate-pending__name {
  font-size: 16px;
  font-weight: 600;
  color: var(--text-color-primary);
}
.startup-gate-pending__spinner {
  font-size: 32px;
  color: var(--el-color-primary);
  animation: startup-gate-spin 1s linear infinite;
}
.startup-gate-pending__text {
  margin: 0;
  font-size: 14px;
  color: var(--text-color-secondary);
}
@keyframes startup-gate-spin {
  from {
    transform: rotate(0deg);
  }
  to {
    transform: rotate(360deg);
  }
}
@media (prefers-reduced-motion: reduce) {
  .startup-gate-pending__spinner {
    animation: none;
  }
}
</style>
