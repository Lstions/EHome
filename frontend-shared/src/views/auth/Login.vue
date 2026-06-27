<template>
  <div class="login-container">
    <!-- 动态背景粒子 -->
    <div class="login-bg">
      <div class="bg-circle c1"></div>
      <div class="bg-circle c2"></div>
      <div class="bg-circle c3"></div>
    </div>

    <div class="login-box">
      <!-- 品牌 Logo -->
      <div class="brand">
        <img src="/favicon.svg" alt="EHomeSystem" class="brand-logo" />
        <div class="brand-text">
          <h1 class="brand-name">EHomeSystem</h1>
          <p class="brand-desc">家庭数字化 · IoT 数据采集平台</p>
        </div>
      </div>

      <!-- 锁定提示 -->
      <el-alert
        v-if="lockSeconds > 0"
        type="warning"
        :closable="false"
        show-icon
        style="margin-bottom: 16px;"
      >
        <template #title>
          登录已锁定，请 <strong>{{ lockSeconds }}</strong> 秒后重试
        </template>
      </el-alert>

      <!-- 错误提示 -->
      <el-alert
        v-if="errorMsg"
        :title="errorMsg"
        type="error"
        show-icon
        :closable="true"
        style="margin-bottom: 16px"
        @close="errorMsg = ''"
      />

      <!-- 登录表单 -->
      <LoginForm
        ref="loginFormRef"
        :disabled="lockSeconds > 0"
        @success="handleLogin"
        @error="handleError"
      />

      <!-- 底部信息 -->
      <div class="login-footer">
        <span class="version">v{{ appVersion }}</span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useUserStore } from '@/stores/user'
import { getRemainingLockSeconds } from '@/utils/loginLockout'
import LoginForm from '@/components/forms/LoginForm.vue'

const router = useRouter()
const route = useRoute()
const userStore = useUserStore()
const loginFormRef = ref()
const errorMsg = ref('')
const lockSeconds = ref(0)
let lockTimer: ReturnType<typeof setInterval> | null = null

const appVersion = computed(() => import.meta.env.VITE_APP_VERSION || '2.0.0')

const refreshLock = () => {
  lockSeconds.value = getRemainingLockSeconds()
  if (lockSeconds.value <= 0 && lockTimer) {
    clearInterval(lockTimer)
    lockTimer = null
  }
}

onMounted(() => {
  refreshLock()
  if (lockSeconds.value > 0) {
    lockTimer = setInterval(refreshLock, 1000)
  }
})

onUnmounted(() => {
  if (lockTimer) clearInterval(lockTimer)
})

const handleLogin = async (username: string, password: string, rememberMe: boolean) => {
  errorMsg.value = ''

  // 前端锁定检查
  if (lockSeconds.value > 0) {
    loginFormRef.value?.setLoading?.(false)
    return
  }

  try {
    await userStore.login(username, password, rememberMe)
    // 跳转到 redirect 参数或首页
    const redirect = (route.query.redirect as string) || '/dashboard'
    router.push(redirect)
  } catch (error: any) {
    // 记录失败次数
    const result = userStore.recordLoginFailure()
    if (result.locked) {
      errorMsg.value = `连续 ${result.attempts} 次登录失败，已锁定 ${Math.ceil(result.remainingSeconds / 60)} 分钟`
      lockSeconds.value = result.remainingSeconds
      lockTimer = setInterval(refreshLock, 1000)
    } else {
      const remaining = 5 - result.attempts
      errorMsg.value = remaining > 0
        ? `用户名或密码错误，还剩 ${remaining} 次机会`
        : error.message || '登录失败'
    }
  } finally {
    loginFormRef.value?.setLoading?.(false)
  }
}

const handleError = (error: any) => {
  errorMsg.value = error.message || '登录失败'
}
</script>

<style scoped>
.login-container {
  display: flex;
  justify-content: center;
  align-items: center;
  min-height: 100vh;
  background: linear-gradient(135deg, #1a1f2e 0%, #2d3548 50%, #1a1f2e 100%);
  overflow: hidden;
  position: relative;
}

/* 动态背景 */
.login-bg {
  position: absolute;
  inset: 0;
  overflow: hidden;
  pointer-events: none;
}
.bg-circle {
  position: absolute;
  border-radius: 50%;
  filter: blur(80px);
  opacity: 0.15;
}
.c1 {
  width: 400px; height: 400px;
  background: #409eff;
  top: -100px; right: -80px;
  animation: float1 12s ease-in-out infinite;
}
.c2 {
  width: 300px; height: 300px;
  background: #67c23a;
  bottom: -60px; left: -40px;
  animation: float2 15s ease-in-out infinite;
}
.c3 {
  width: 200px; height: 200px;
  background: #e6a23c;
  top: 50%; left: 50%;
  transform: translate(-50%, -50%);
  animation: float3 10s ease-in-out infinite;
}
@keyframes float1 {
  0%, 100% { transform: translate(0, 0); }
  50% { transform: translate(-30px, 30px); }
}
@keyframes float2 {
  0%, 100% { transform: translate(0, 0); }
  50% { transform: translate(20px, -20px); }
}
@keyframes float3 {
  0%, 100% { transform: translate(-50%, -50%) scale(1); }
  50% { transform: translate(-50%, -50%) scale(1.2); }
}

.login-box {
  width: 420px;
  padding: 48px 40px 32px;
  background: rgba(255, 255, 255, 0.97);
  border-radius: 16px;
  box-shadow: 0 8px 40px rgba(0, 0, 0, 0.2);
  z-index: 1;
  position: relative;
}

.brand {
  display: flex;
  align-items: center;
  gap: 16px;
  margin-bottom: 36px;
}
.brand-logo {
  width: 52px;
  height: 52px;
  flex-shrink: 0;
}
.brand-text {
  flex: 1;
}
.brand-name {
  margin: 0;
  font-size: 24px;
  font-weight: 700;
  color: #303133;
  letter-spacing: -0.5px;
}
.brand-desc {
  margin: 4px 0 0;
  font-size: 13px;
  color: #909399;
  letter-spacing: 0.5px;
}

.login-footer {
  margin-top: 24px;
  text-align: center;
}
.version {
  font-size: 12px;
  color: #c0c4cc;
}
</style>
