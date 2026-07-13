import { createApp } from 'vue'
import { createPinia } from 'pinia'
import 'element-plus/theme-chalk/dark/css-vars.css'
// ElMessageBox 通过 JS API 调用时，按需引入不会自动加载其 CSS，需手动导入
import 'element-plus/theme-chalk/el-message-box.css'
import './styles/theme.css'
import router from './router'
import i18n from './locales'
import App from './App.vue'
import permissionDirective from '@/directives/permission'
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

// 注册自定义指令
app.directive('permission', permissionDirective)

app.use(createPinia())
app.use(router)
app.use(i18n)

logger.info('应用挂载', { element: '#app' })
app.mount('#app')
