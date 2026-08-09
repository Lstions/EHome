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

      <!-- 首次运行成功提示 -->
      <el-alert
        v-if="successMsg"
        :title="successMsg"
        type="success"
        show-icon
        :closable="false"
        style="margin-bottom: 16px"
      />

      <!-- 首次运行管理员设置 -->
      <InitializeAdminForm
        v-if="authState === 'uninitialized'"
        ref="initializeFormRef"
        @submit="handleInitialize"
      />

      <!-- 登录表单 -->
      <LoginForm
        v-else
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

    <!-- 登录成功后的全屏品牌过渡层：掩盖懒加载 chunk（echarts/element 等）下载期间的空窗 -->
    <div v-if="showTransition" class="login-transition" aria-live="polite">
      <img src="/favicon.svg" alt="" class="login-transition__logo" />
      <p class="login-transition__text">正在进入系统</p>
      <div class="login-transition__track">
        <div class="login-transition__bar" />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useUserStore } from '@/stores/user'
import { authApi, type AuthState, type InitializeRequest } from '@/api/auth'
import { getRemainingLockSeconds } from '@/utils/loginLockout'
import LoginForm from '@/components/forms/LoginForm.vue'
import InitializeAdminForm from '@/components/forms/InitializeAdminForm.vue'
// 登录后待跳转主框架的懒加载 chunk 预取：与 router 懒加载同一 chunk，
// 登录成功后立即 hot 命中缓存，大幅缩短主界面首帧等待。
const prefetchMainLayout = () => import('@/views/layout/MainLayout.vue')
const prefetchDashboard = () => import('@/views/dashboard/Dashboard.vue')

const router = useRouter()
const route = useRoute()
const userStore = useUserStore()
const loginFormRef = ref()
const initializeFormRef = ref()
const errorMsg = ref('')
const successMsg = ref('')
const showTransition = ref(false)
const authState = ref<AuthState | null>(null)
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

const statusMessages: Record<AuthState, string> = {
  initialized: '',
  migration_required: '系统认证状态异常，请通过本机运维流程恢复。',
  uninitialized: '系统尚未初始化，请先使用本机初始化凭据完成初始化。',
  disabled: '系统认证已被禁用，请联系管理员。',
}

onMounted(() => {
  void authApi.initialization().then((status) => {
    authState.value = status.state
    if (status.state !== 'initialized' && status.state !== 'uninitialized') {
      errorMsg.value = statusMessages[status.state]
    }
  }).catch(() => {
    errorMsg.value = '无法读取系统认证状态，请检查开发后端。'
  })
  refreshLock()
  if (lockSeconds.value > 0) {
    lockTimer = setInterval(refreshLock, 1000)
  }
  // 非阻塞预取：target=nextIdle 保证不抢登录页首帧；catch 兜底避免拆包缺失时抛错
  if (typeof requestIdleCallback === 'function') {
    window.requestIdleCallback(() => {
      prefetchMainLayout().catch(() => {})
      prefetchDashboard().catch(() => {})
    })
  }
})

onUnmounted(() => {
  if (lockTimer) clearInterval(lockTimer)
})

const handleLogin = async (username: string, password: string, rememberMe: boolean) => {
  // 1. 先清空前序错误，避免旧状态误导用户
  errorMsg.value = ''
  // 2. 锁定检查
  if (lockSeconds.value > 0) {
    loginFormRef.value?.setLoading?.(false)
    return
  }
  // 3. 系统未就绪时直接给出明确提示，不调用登录 API
  if (authState.value && authState.value !== 'initialized') {
    errorMsg.value = statusMessages[authState.value]
    loginFormRef.value?.setLoading?.(false)
    return
  }

  try {
    await userStore.login(username, password, rememberMe)
    // 跳转到 redirect 参数或首页
    const redirect = (route.query.redirect as string) || '/dashboard'
    // 先展示全屏过渡层，再触发路由跳转：懒加载 chunk 下载期间用户看到的
    // 是品牌过渡动画而非冻结的登录表单。
    showTransition.value = true
    try {
      await router.push(redirect)
    } catch (navigateError: any) {
      // chunk 加载失败等导航错误：收起过渡层并提示重试，避免白屏卡死
      showTransition.value = false
      errorMsg.value = '页面加载失败，请刷新重试'
      // 继续抛出，交给外层统一处理登录态；失败场景下不重复弹提示
      throw navigateError
    }
  } catch (error: any) {
    const status = Number(error?.status ?? error?.response?.status ?? error?.code)

    // 服务端限流不是凭据错误，不应继续累加浏览器端失败次数。
    if (status === 429) {
      const retryAfter = Number(error?.retryAfterSeconds)
      errorMsg.value = retryAfter > 0
        ? `登录尝试过于频繁，请 ${formatRetryAfter(retryAfter)}后重试。`
        : '登录尝试过于频繁，请稍后重试。'
      return
    }

    // 只有明确的 401 凭据错误才计入前端失败次数。网络错误、500 等
    // 服务异常应直接反馈原因，避免误导用户修改正确密码。
    if (status !== 401) {
      errorMsg.value = getLoginErrorMessage(error, status)
      return
    }

    // 记录凭据失败次数
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

const formatRetryAfter = (seconds: number): string => {
  if (seconds >= 60) return `${Math.ceil(seconds / 60)} 分钟`
  return `${Math.max(1, Math.ceil(seconds))} 秒`
}

const getLoginErrorMessage = (error: any, status: number): string => {
  if (error?.code === 'ERR_NETWORK' || error?.message === 'Network Error') {
    return '无法连接服务器，请检查网络连接后重试。'
  }
  if (error?.code === 'ECONNABORTED' || error?.code === 'ETIMEDOUT') {
    return '登录请求超时，请稍后重试。'
  }
  if (status >= 500) {
    return '服务器暂时不可用，请稍后重试。'
  }
  return error?.message || '登录失败，请稍后重试。'
}

const handleError = (error: any) => {
  errorMsg.value = error.message || '登录失败'
}

const handleInitialize = async (request: InitializeRequest) => {
  errorMsg.value = ''
  successMsg.value = ''

  try {
    await authApi.initialize(request)
    authState.value = 'initialized'
    successMsg.value = '管理员账号创建成功，请使用新账号登录。'
    initializeFormRef.value?.resetForm?.()
  } catch (error: any) {
    errorMsg.value = error?.message || '初始化失败，请检查初始化凭据后重试。'
  } finally {
    initializeFormRef.value?.setLoading?.(false)
  }
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
  background: var(--el-color-primary);
  top: -100px; right: -80px;
  animation: float1 12s ease-in-out infinite;
}
.c2 {
  width: 300px; height: 300px;
  background: var(--el-color-success);
  bottom: -60px; left: -40px;
  animation: float2 15s ease-in-out infinite;
}
.c3 {
  width: 200px; height: 200px;
  background: var(--el-color-warning);
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
  width: min(420px, calc(100vw - 24px));
  padding: 48px 40px 32px;
  background: color-mix(in srgb, var(--card-bg) 97%, transparent);
  border-radius: 16px;
  box-shadow: var(--shadow-lg);
  z-index: 1;
  position: relative;
  box-sizing: border-box;
}

@media (max-width: 480px) {
  .login-box {
    padding: 32px 20px 24px;
  }
  .brand {
    gap: 12px;
    margin-bottom: 24px;
  }
  .brand-logo {
    width: 44px;
    height: 44px;
  }
  .brand-name {
    font-size: 20px;
  }
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
  color: var(--text-color-primary);
  letter-spacing: -0.5px;
}
.brand-desc {
  margin: 4px 0 0;
  font-size: 13px;
  color: var(--el-text-color-secondary);
  letter-spacing: 0.5px;
}

.login-footer {
  margin-top: 24px;
  text-align: center;
}
.version {
  font-size: 12px;
  color: var(--el-text-color-placeholder);
}

/* 登录成功后的全屏品牌过渡层 */
.login-transition {
  position: fixed;
  inset: 0;
  z-index: 5000; /* 高于顶部进度条 (4000) */
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 24px;
  background: linear-gradient(135deg, #1a1f2e 0%, #2d3548 50%, #1a1f2e 100%);
}
.login-transition__logo {
  width: 72px;
  height: 72px;
  animation: transitionPulse 1.6s ease-in-out infinite;
}
.login-transition__text {
  margin: 0;
  font-size: 16px;
  letter-spacing: 4px;
  color: rgba(255, 255, 255, 0.85);
}
.login-transition__track {
  width: min(220px, 60vw);
  height: 3px;
  border-radius: 3px;
  background: rgba(255, 255, 255, 0.15);
  overflow: hidden;
}
.login-transition__bar {
  height: 100%;
  border-radius: 3px;
  /* 不定长循环：中段往返扩张，视觉上始终在加载 */
  background: linear-gradient(90deg, var(--el-color-primary-light-5), var(--el-color-primary));
  animation: transitionBar 1.4s ease-in-out infinite;
}
@keyframes transitionPulse {
  0%, 100% { transform: scale(1); opacity: 0.9; }
  50% { transform: scale(1.06); opacity: 1; }
}
@keyframes transitionBar {
  0% { width: 0%; margin-left: 0; }
  40% { width: 45%; margin-left: 5%; }
  100% { width: 55%; margin-left: 100%; }
}
</style>
