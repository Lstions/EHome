<template>
  <div class="error-page not-found">
    <div class="error-content">
      <div class="error-code">404</div>
      <h1 class="error-title">页面不存在</h1>
      <p class="error-desc">您访问的页面已被移除、重命名或暂时不可用。</p>
      <div class="error-actions">
        <el-button type="primary" :icon="HomeFilled" @click="goHome">返回首页</el-button>
        <el-button :icon="Back" @click="goBack">返回上页</el-button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useRouter } from 'vue-router'
import { HomeFilled, Back } from '@element-plus/icons-vue'

const router = useRouter()

const goHome = () => router.push('/dashboard')
const goBack = () => {
  if (window.history.length > 1) {
    router.back()
  } else {
    router.push('/dashboard')
  }
}
</script>

<style scoped>
.error-page {
  display: flex;
  justify-content: center;
  align-items: center;
  min-height: 100vh;
  background: var(--bg-color-page);
  padding: 24px;
}
.error-content {
  text-align: center;
  max-width: 480px;
}
.error-code {
  font-size: 120px;
  font-weight: 700;
  line-height: 1;
  background: linear-gradient(135deg, var(--el-color-primary), var(--el-color-success));
  -webkit-background-clip: text;
  -webkit-text-fill-color: transparent;
  background-clip: text;
  margin-bottom: 16px;
}
.error-title {
  font-size: 24px;
  font-weight: 600;
  color: var(--text-color-primary);
  margin: 0 0 12px;
}
.error-desc {
  font-size: 14px;
  color: var(--text-color-secondary);
  margin: 0 0 32px;
  line-height: 1.6;
}
.error-actions {
  display: flex;
  gap: 12px;
  justify-content: center;
}
</style>
