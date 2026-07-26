<template>
  <ErrorPageLayout
    code="404"
    title="页面不存在"
    description="您访问的页面已被移除、重命名或暂时不可用。"
  >
    <template #actions>
      <el-button type="primary" :icon="HomeFilled" @click="goHome">返回首页</el-button>
      <el-button :icon="Back" @click="goBack">返回上页</el-button>
    </template>
  </ErrorPageLayout>
</template>

<script setup lang="ts">
import { useRouter } from 'vue-router'
import { HomeFilled, Back } from '@element-plus/icons-vue'
import ErrorPageLayout from '@/components/common/ErrorPageLayout.vue'

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
