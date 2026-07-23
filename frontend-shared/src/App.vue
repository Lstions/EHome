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
/* 移动端字体缩放 */
@media (max-width: 768px) {
  html {
    font-size: 15px;
  }
}

@media (max-width: 480px) {
  html {
    font-size: 16px;
  }
}

/* 移动端 Element Plus 组件字体缩放 */
@media (max-width: 768px) {
  .el-input__inner,
  .el-select .el-input__inner,
  .el-textarea__inner {
    font-size: 15px;
  }
  .el-button {
    font-size: 14px;
  }
  .el-form-item__label {
    font-size: 14px;
  }
  .el-table {
    font-size: 14px;
  }
  .el-card {
    font-size: 14px;
  }
  .el-tag {
    font-size: 13px;
  }
  .el-pagination {
    font-size: 13px;
  }
  .el-menu {
    font-size: 15px;
  }
  .el-tabs__item {
    font-size: 14px;
  }
  .el-dropdown-menu__item {
    font-size: 14px;
  }
}

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
</style>
