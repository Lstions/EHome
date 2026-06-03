<template>
  <div class="error-page forbidden">
    <div class="error-content">
      <div class="error-code">403</div>
      <h1 class="error-title">无权访问</h1>
      <p class="error-desc">
        当前账号 <strong>{{ username }}</strong> 角色为
        <el-tag :type="roleTagType" size="default" class="role-tag">{{ roleLabel }}</el-tag>
        ，没有访问该页面的权限。
      </p>
      <p class="error-hint">如需访问该功能，请联系管理员调整角色权限。</p>
      <div class="error-actions">
        <el-button type="primary" :icon="HomeFilled" @click="goHome">返回首页</el-button>
        <el-button :icon="SwitchButton" @click="relogin">切换账号</el-button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { useUserStore } from '@/stores/user'
import { HomeFilled, SwitchButton } from '@element-plus/icons-vue'

const router = useRouter()
const userStore = useUserStore()

const username = computed(() => userStore.userInfo?.username || '当前用户')
const role = computed(() => userStore.userInfo?.role || 'viewer')
const roleLabel = computed(() => {
  const map: Record<string, string> = {
    admin: '管理员',
    operator: '操作员',
    viewer: '观察者',
  }
  return map[role.value] || role.value
})
const roleTagType = computed<'danger' | 'warning' | 'info'>(() => {
  if (role.value === 'admin') return 'danger'
  if (role.value === 'operator') return 'warning'
  return 'info'
})

const goHome = () => router.push('/dashboard')
const relogin = async () => {
  await userStore.logout()
  router.push('/login')
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
  max-width: 520px;
}
.error-code {
  font-size: 120px;
  font-weight: 700;
  line-height: 1;
  background: linear-gradient(135deg, #f56c6c, #e6a23c);
  -webkit-background-clip: text;
  -webkit-text-fill-color: transparent;
  background-clip: text;
  margin-bottom: 16px;
}
.error-title {
  font-size: 24px;
  font-weight: 600;
  color: var(--text-color-primary);
  margin: 0 0 16px;
}
.error-desc {
  font-size: 14px;
  color: var(--text-color-regular);
  margin: 0 0 8px;
  line-height: 1.8;
}
.role-tag {
  vertical-align: middle;
  margin: 0 4px;
}
.error-hint {
  font-size: 13px;
  color: var(--text-color-secondary);
  margin: 0 0 32px;
}
.error-actions {
  display: flex;
  gap: 12px;
  justify-content: center;
}
</style>
