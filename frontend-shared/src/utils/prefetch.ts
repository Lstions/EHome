/**
 * P0-A：首跳 chunk 预取。
 *
 * 字面量 import 路径必须与 src/router/index.ts 的懒加载定义完全一致，
 * 保证命中同一 chunk——用户停留在登录页的几秒钟里后台下载完成，
 * 登录成功瞬间 router.push 无需再等 chunk。
 *
 * 抽为独立模块（而非 Login.vue 内联箭头函数）：
 * 1. 单测可 vi.mock 本模块精确断言预取被调度（内联动态 import 无法被 mock 记录）；
 * 2. Vite 构建时仍识别为独立动态入口，产物分包不变。
 */
export const prefetchMainLayout = () => import('@/views/layout/MainLayout.vue')
export const prefetchDashboard = () => import('@/views/dashboard/Dashboard.vue')
