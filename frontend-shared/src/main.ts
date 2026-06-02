import { createApp } from 'vue'
import { createPinia } from 'pinia'
import ElementPlus from 'element-plus'
import 'element-plus/dist/index.css'
import 'element-plus/theme-chalk/dark/css-vars.css'
import * as ElementPlusIconsVue from '@element-plus/icons-vue'
import router from './router'
import App from './App.vue'
import '@/styles/theme.css'
import { logger } from '@/utils/logger'

// 初始化日志系统
logger.info('应用启动', {
  version: import.meta.env.VITE_APP_VERSION || '1.1.0',
  mode: import.meta.env.MODE
})

// 全局错误处理
const app = createApp(App)

app.config.errorHandler = (err, instance, info) => {
  logger.error('Vue错误', {
    error: String(err),
    component: instance?.$options?.name || 'Unknown',
    info
  })
}

app.config.warnHandler = (msg, instance, trace) => {
  logger.warn('Vue警告', {
    message: msg,
    component: instance?.$options?.name || 'Unknown',
    trace
  })
}

// 注册所有 Element Plus 图标
for (const [key, component] of Object.entries(ElementPlusIconsVue)) {
  app.component(key, component)
}

app.use(createPinia())
app.use(router)
app.use(ElementPlus)

logger.info('应用挂载', { element: '#app' })
app.mount('#app')
