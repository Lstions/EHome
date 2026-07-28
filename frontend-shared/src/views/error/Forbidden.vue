<template>
  <ErrorPageLayout
    code="403"
    title="无权访问"
    gradient="linear-gradient(135deg, var(--el-color-danger), var(--el-color-warning))"
    max-width="520px"
  >
    <template #description>
      <p style="margin: 0 auto 8px; max-width: 400px; line-height: 1.8; color: var(--text-color-regular);">
        当前账号 <strong>{{ username }}</strong>（<el-tag type="primary" size="default" style="vertical-align: middle;">系统管理员</el-tag>）没有访问该页面的权限。
      </p>
      <p style="font-size: 13px; color: var(--text-color-secondary); margin: 0;">如需访问该功能，请联系系统管理员调整角色权限。</p>
    </template>
    <template #actions>
      <el-button type="primary" :icon="HomeFilled" @click="goHome">返回首页</el-button>
      <el-button :icon="SwitchButton" @click="relogin">切换账号</el-button>
    </template>
  </ErrorPageLayout>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { useUserStore } from '@/stores/user'
import { HomeFilled, SwitchButton } from '@element-plus/icons-vue'
import ErrorPageLayout from '@/components/common/ErrorPageLayout.vue'

const router = useRouter()
const userStore = useUserStore()

const username = computed(() => userStore.userInfo?.username || '当前用户')

const goHome = () => router.push('/dashboard')
const relogin = async () => {
  await userStore.logout()
  router.push('/login')
}
</script>
