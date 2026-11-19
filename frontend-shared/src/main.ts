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
import { notifyModuleLoadFailure } from '@/stores/appUpdate'

/**
 * 构建身份（T2.4）：**不再有任何写死的回退常量**。
 * 版本号由构建期注入（vite.config.ts 的 define），缺失时展示「未知」——
 * 而不是偷偷显示 2.0.0 / 2.2.0 这类与镜像无关的字符串（那会让「侧栏版本」
 * 变成永远正确又永远无用的装饰）。
 */
const buildId = (import.meta.env.VITE_BUILD_ID || '').trim()
const buildTime = (import.meta.env.VITE_BUILD_TIME || '').trim()
const appVersion = (import.meta.env.VITE_APP_VERSION || '').trim()

// 初始化日志系统
logger.info('应用启动', {
  version: appVersion || '未知',
  buildId: buildId || '(none)',
  buildTime: buildTime || '(none)',
  mode: import.meta.env.MODE,
  name: import.meta.env.VITE_APP_NAME || 'EHomeSystem',
})

/**
 * 对外暴露构建身份：排障与 e2e 用（E2E-04..10 需要先确认被测页面是**构建产物**，
 * 开发服务器没有 build id，此时更新检测按设计不提示，用例应显式 skip 而不是假红）。
 */
Object.defineProperty(window, '__EHOME_BUILD__', {
  value: { id: buildId, time: buildTime },
  writable: false,
  configurable: false,
})

/**
 * 通道 4 之一：`vite:preloadError`。
 *
 * 懒加载路由引用的旧 hash 在部署后已不存在 ⇒ 动态 import 失败。Vite 会派发
 * `vite:preloadError`（可 preventDefault 阻止默认抛错）。此处**不**阻止默认行为：
 * 我们只负责把状态推进 `blocked` 弹出不可推迟的刷新模态；让错误继续上抛，
 * ErrorBoundary / 控制台仍能看到完整堆栈（排障优先）。
 *
 * 注意：该事件在**模块副作用里注册**（早于 app.mount），否则首屏那一次
 * 预加载失败会赶在监听器安装之前发生。
 */
window.addEventListener('vite:preloadError', (event) => {
  const detail = String((event as Event & { payload?: unknown }).payload ?? 'vite:preloadError')
  logger.warn('检测到预加载资源失效', { detail })
  notifyModuleLoadFailure(detail)
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
