import { createApp } from 'vue'
import { createPinia } from 'pinia'
import 'element-plus/theme-chalk/dark/css-vars.css'
// ElMessageBox / ElMessage 通过 JS API 调用时，按需引入（unplugin-vue-components）
// 不会自动加载其 CSS，必须手动导入：
//   · el-message-box.css —— 确认框
//   · el-message.css    —— 轻提示（2026-10-21 实测：缺失时 .el-message 计算样式为
//     position:static，被追加到 body 末尾 ⇒ 渲染在视口下方（rect.y=851/900，body 因
//     弹窗 scroll-lock 不可滚），**所有 ElMessage 提示用户都看不到** —— 校验失败、
//     保存成功等反馈全部静默。属于全局缺陷，根因在入口，故在此修。）
import 'element-plus/theme-chalk/el-message-box.css'
import 'element-plus/theme-chalk/el-message.css'
import './styles/theme.css'
import router from './router'
import App from './App.vue'

import { logger } from '@/utils/logger'
import { initLoginLockout } from '@/utils/loginLockout'

// 初始化日志系统
logger.info('应用启动', {
  version: import.meta.env.VITE_APP_VERSION || '2.0.0',
  mode: import.meta.env.MODE,
  name: import.meta.env.VITE_APP_NAME || 'EHomeSystem',
})

// 初始化登录锁定（清掉过期的锁定记录）
initLoginLockout()

// Element Plus 按需引入由 unplugin-vue-components/unplugin-auto-import 自动处理
// 主题样式也由 autoImportCSS 注入
// 这里只需要保留 dark 主题

const app = createApp(App)

// 全局错误处理
app.config.errorHandler = (err, instance, info) => {
  logger.error('Vue错误', {
    error: err instanceof Error ? `${err.message}\n${err.stack}` : String(err),
    component: instance?.$options?.name || 'Unknown',
    info,
  })
}

app.config.warnHandler = (msg, instance, trace) => {
  // 抑制 Element Plus 已知警告（如重复组件名）
  if (msg.includes('Duplicate' as any)) return
  logger.warn('Vue警告', {
    message: msg,
    component: instance?.$options?.name || 'Unknown',
    trace,
  })
}


app.use(createPinia())
app.use(router)

logger.info('应用挂载', { element: '#app' })
app.mount('#app')
