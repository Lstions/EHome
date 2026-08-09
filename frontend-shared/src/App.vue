<template>
  <el-config-provider :locale="locale">
    <ErrorBoundary>
      <NetworkBanner />
      <!-- 全局路由进度条：懒加载 chunk 下载期间保持可见反馈。ErrorBoundary 之前，确保路由级错误也能被覆盖 -->
      <RouteProgressBar />
      <router-view />
    </ErrorBoundary>
  </el-config-provider>
</template>

<script setup lang="ts">
import { watch } from 'vue'
import { useThemeStore } from '@/stores/theme'
import zhCn from 'element-plus/es/locale/lang/zh-cn'
import ErrorBoundary from '@/components/common/ErrorBoundary.vue'
import NetworkBanner from '@/components/common/NetworkBanner.vue'
import RouteProgressBar from '@/components/common/RouteProgressBar.vue'

const themeStore = useThemeStore()

const locale = zhCn

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
</style>
