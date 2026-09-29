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
import { loginPath } from '@/utils/basePath'

const wsStore = useWebSocketStore()
const visible = ref(false)
const message = ref('')
const retryable = ref(true)
const offlineByBrowser = ref(false)

const iconComp = computed(() => {
  if (offlineByBrowser.value) return CircleCloseFilled
  // 浏览器在线但 WebSocket 连接断开 → 警告图标（lastError 字段不存在于
  // websocket store，曾导致本分支永远 falsy、警告图标永不显示）
  if (!wsStore.connected) return WarningFilled
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
    window.location.href = loginPath()
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
  font-size: 14px;
  box-shadow: var(--shadow-md);
}
/* 离线/重连横幅是**用户最需要看清**的通知，必须满足正文 4.5:1。
   改前是「语义实心渐变 + 白字」：亮色下成立（5.42/6.47），但暗色主题把
   warning/danger 提亮成 #ebb563 / #f78989，白字塌到 **1.85 / 2.36:1** —— 暗色下几乎不可读。
   改为「-light-9 浅底 + 基色文字」：这是项目 el-tag/el-alert 的既有范式
   （见 theme.css 顶部「浅底徽标的文字色」桥接说明），实测亮 4.74/5.45、暗 8.47/6.88，
   两主题均稳过 4.5。基色在亮/暗两块本就是按「对浅底达标」选的，故文字直接用基色。 */
.network-banner.error {
  background: var(--el-color-danger-light-9);
  color: var(--el-color-danger);
  border-bottom: 1px solid var(--el-color-danger-light-7);
}
.network-banner.warning {
  background: var(--el-color-warning-light-9);
  color: var(--el-color-warning);
  border-bottom: 1px solid var(--el-color-warning-light-7);
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
