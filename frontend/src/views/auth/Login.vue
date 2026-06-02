<template>
  <div class="login-container">
    <div class="login-box">
      <h1 class="login-title">HomeStation</h1>
      <p class="login-subtitle">IoT 数据采集系统</p>

      <el-alert
        v-if="errorMsg"
        :title="errorMsg"
        type="error"
        show-icon
        :closable="true"
        style="margin-bottom: 16px"
        @close="errorMsg = ''"
      />

      <LoginForm
        ref="loginFormRef"
        @success="handleLogin"
        @error="handleError"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { useUserStore } from '@/stores/user'
import LoginForm from '@/components/forms/LoginForm.vue'

const router = useRouter()
const userStore = useUserStore()
const loginFormRef = ref()
const errorMsg = ref('')

const handleLogin = async (username: string, password: string, rememberMe: boolean) => {
  errorMsg.value = ''
  try {
    await userStore.login(username, password, rememberMe)
    router.push('/dashboard')
  } catch (error: any) {
    console.error('Login error:', error)
    errorMsg.value = error.message || error.response?.data?.message || '用户名或密码错误'
  } finally {
    loginFormRef.value?.setLoading(false)
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
  background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
}

.login-box {
  width: 400px;
  padding: 40px;
  background: #fff;
  border-radius: 8px;
  box-shadow: 0 4px 20px rgba(0, 0, 0, 0.1);
}

.login-title {
  margin: 0 0 8px;
  font-size: 28px;
  font-weight: 600;
  color: #303133;
  text-align: center;
}

.login-subtitle {
  margin: 0 0 32px;
  font-size: 14px;
  color: #909399;
  text-align: center;
}
</style>
