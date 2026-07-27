<template>
  <el-config-provider :locale="locale">
    <ErrorBoundary>
      <NetworkBanner />
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
</style>
