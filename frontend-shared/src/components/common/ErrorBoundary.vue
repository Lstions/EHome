<template>
  <div v-if="error" class="error-boundary">
    <div class="error-content">
      <el-result icon="error" :title="error.name || '组件错误'" :sub-title="error.message">
        <template #extra>
          <el-button type="primary" @click="handleReset">重试</el-button>
          <el-button @click="handleReload">刷新页面</el-button>
        </template>
      </el-result>
      <details v-if="error.stack" class="error-details">
        <summary>错误堆栈</summary>
        <pre>{{ error.stack }}</pre>
      </details>
    </div>
  </div>
  <slot v-else />
</template>

<script setup lang="ts">
import { ref, onErrorCaptured } from 'vue'
import { logger } from '@/utils/logger'

const error = ref<Error | null>(null)

onErrorCaptured((err: unknown) => {
  const e = err instanceof Error ? err : new Error(String(err))
  error.value = e
  logger.error('ErrorBoundary 捕获组件错误', {
    name: e.name,
    message: e.message,
    stack: e.stack,
  })
  // 阻止错误继续冒泡
  return false
})

const handleReset = () => {
  error.value = null
}

const handleReload = () => {
  window.location.reload()
}
</script>

<style scoped>
.error-boundary {
  min-height: 400px;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 40px;
}
.error-content {
  max-width: 720px;
  width: 100%;
}
.error-details {
  margin-top: 16px;
  background: #f5f7fa;
  padding: 12px 16px;
  border-radius: 4px;
  font-size: 12px;
}
.error-details summary {
  cursor: pointer;
  color: #606266;
  font-weight: 500;
  user-select: none;
}
.error-details pre {
  margin: 8px 0 0;
  white-space: pre-wrap;
  word-break: break-all;
  color: #f56c6c;
  max-height: 240px;
  overflow-y: auto;
}
</style>
