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

interface RouterHistoryState {
  back?: unknown
  current?: unknown
}

/**
 * 「本应用是否留下过可回退的历史记录」。
 *
 * 为什么不能用 window.history.length > 1：新标签页直接输入一个不存在的路由时，
 * 浏览器历史里已经有一条空白起始记录（history.length === 2），判据为真，
 * back() 会把用户送到 about:blank。
 *
 * 改用 vue-router 自己维护的 history state 标记：路由每次 push/replace 都会把
 * { back, current, forward, position } 写进 history.state（vue-router dist/buildState）。
 * - 首次进入应用（新标签页 / 外链 / 书签）时 back 为 null；
 * - 只要发生过一次应用内跳转，back 就是上一条应用内 URL。
 * 因此「current 是字符串（确认是本应用的 state）+ back 是非空字符串」才认为可以安全回退。
 *
 * 覆盖场景：应用内跳转到 404 → back() 回到上一页；
 * 直接输入 404 地址/刷新 404 页面 → 回 /dashboard（而不是 about:blank）。
 * 不覆盖场景：从外部站点整页跳到 404 时（back 为 null），不会回外部站点，而是回 /dashboard
 * —— 这是保守选择，避免 back() 落到空白页或把用户带出应用。
 */
const canGoBackWithinApp = (): boolean => {
  const state = window.history.state as RouterHistoryState | null
  if (!state || typeof state !== 'object') return false
  return typeof state.current === 'string' && typeof state.back === 'string' && state.back !== ''
}

const goBack = () => {
  if (canGoBackWithinApp()) {
    router.back()
  } else {
    router.push('/dashboard')
  }
}
</script>
