<template>
  <transition name="slide-down">
    <div v-if="visible" class="network-banner" :class="severity">
      <el-icon class="banner-icon"><component :is="iconComp" /></el-icon>
      <div class="banner-content">
        <strong>{{ title }}</strong>
        <span v-if="message" class="banner-msg">{{ message }}</span>
      </div>
      <el-button v-if="retryable" link size="small" @click="onRetry">重试</el-button>
    </div>
  </transition>
</template>

<script setup lang="ts">
import { computed, ref, onMounted, onUnmounted } from 'vue'
import { CircleCloseFilled, WarningFilled, Link } from '@element-plus/icons-vue'
import { useWebSocketStore } from '@/stores/websocket'

const wsStore = useWebSocketStore()
const visible = ref(false)
const message = ref('')
const retryable = ref(true)
const offlineByBrowser = ref(false)

const iconComp = computed(() => {
  if (offlineByBrowser.value) return CircleCloseFilled
  if (wsStore.lastError) return WarningFilled
  return Link
})

const severity = computed<'error' | 'warning'>(() => (offlineByBrowser.value ? 'error' : 'warning'))

const title = computed(() => {
  if (offlineByBrowser.value) return '网络已断开'
  // Only show WS disconnected if user is authenticated
  if (wsStore.isAuthenticated && !wsStore.connected) return '与服务器的连接已断开'
  return ''
})

let timer: ReturnType<typeof setTimeout> | null = null
const show = (msg: string, retry = true) => {
  message.value = msg
  retryable.value = retry
  visible.value = true
}
const hide = () => {
  visible.value = false
  message.value = ''
}

const onRetry = () => {
  if (offlineByBrowser.value) {
    window.location.reload()
  } else if (!wsStore.isAuthenticated) {
    // 未登录时跳转到登录页
    window.location.href = '/login'
  } else {
    wsStore.connect()
    hide()
  }
}

const handleOnline = () => {
  offlineByBrowser.value = false
  hide()
}
const handleOffline = () => {
  offlineByBrowser.value = true
  show('请检查您的网络连接', false)
}

let stopWatch: (() => void) | null = null

onMounted(() => {
  window.addEventListener('online', handleOnline)
  window.addEventListener('offline', handleOffline)

  if (!navigator.onLine) {
    handleOffline()
  }

  // 监听 wsStore 状态变化
  stopWatch = wsStore.onConnected(() => hide())
  // 简单定时检查 ws 状态
  timer = setInterval(() => {
    // Only show WS reconnect banner if user is authenticated
    if (wsStore.isAuthenticated && !wsStore.connected && !visible.value && !offlineByBrowser.value) {
      show('正在尝试重新连接...', true)
    }
  }, 10000)
})

onUnmounted(() => {
  window.removeEventListener('online', handleOnline)
  window.removeEventListener('offline', handleOffline)
  if (timer) clearInterval(timer)
  if (stopWatch) stopWatch()
})
</script>

<style scoped>
.network-banner {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  z-index: 9999;
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 24px;
  color: #fff;
  font-size: 14px;
  box-shadow: var(--shadow-md);
}
.network-banner.error {
  background: linear-gradient(90deg, var(--el-color-danger), var(--el-color-danger-light-3));
}
.network-banner.warning {
  background: linear-gradient(90deg, var(--el-color-warning), var(--el-color-warning-light-3));
  color: #fff;
}
.banner-icon {
  font-size: 20px;
}
.banner-content {
  flex: 1;
  display: flex;
  align-items: baseline;
  gap: 8px;
  flex-wrap: wrap;
}
.banner-msg {
  font-size: 13px;
  opacity: 0.9;
}

.slide-down-enter-active,
.slide-down-leave-active {
  transition: transform 0.3s ease, opacity 0.3s ease;
}
.slide-down-enter-from,
.slide-down-leave-to {
  transform: translateY(-100%);
  opacity: 0;
}
</style>
