<template>
  <el-dropdown trigger="click" @command="handleCommand">
    <el-button :icon="currentIcon" circle aria-label="切换主题" />
    <template #dropdown>
      <el-dropdown-menu>
        <el-dropdown-item command="light" :disabled="!themeStore.followsSystem && themeStore.mode === 'light'">
          <el-icon><Sunny /></el-icon>
          <span style="margin-left: 8px">亮色模式</span>
        </el-dropdown-item>
        <el-dropdown-item command="dark" :disabled="!themeStore.followsSystem && themeStore.mode === 'dark'">
          <el-icon><Moon /></el-icon>
          <span style="margin-left: 8px">暗色模式</span>
        </el-dropdown-item>
        <el-dropdown-item divided command="system" :disabled="themeStore.followsSystem">
          <el-icon><Monitor /></el-icon>
          <span style="margin-left: 8px">跟随系统</span>
        </el-dropdown-item>
      </el-dropdown-menu>
    </template>
  </el-dropdown>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { Sunny, Moon, Monitor } from '@element-plus/icons-vue'
import { useThemeStore, type ThemeMode } from '@/stores/theme'

const themeStore = useThemeStore()

const currentIcon = computed(() => {
  return themeStore.mode === 'dark' ? Moon : Sunny
})

const handleCommand = (command: string) => {
  if (command === 'system') {
    themeStore.followSystem()
  } else {
    themeStore.setTheme(command as ThemeMode)
  }
}
</script>

<style scoped>
.el-dropdown-menu__item {
  display: flex;
  align-items: center;
}

/* 移动端页头主题按钮触控目标（规范 §4.4.5 MUST：≥44×44px）。
   circle 按钮默认 32×32，这里把实际盒子补到 44×44（真实布局盒，
   可用 getBoundingClientRect 验收，也能被真实指针与读屏正确识别）。 */
@media (max-width: 768px) {
  .el-button.is-circle {
    min-width: 44px;
    min-height: 44px;
  }
}
</style>
