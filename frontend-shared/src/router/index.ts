import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import { useUserStore } from '@/stores/user'
import { useWebSocketStore } from '@/stores/websocket'

import { loadEdgeDeviceList, loadNodeList } from './routeLoaders'
import { useRouteProgress } from '@/stores/routeProgress'

declare module 'vue-router' {
  interface RouteMeta {
    requiresAuth?: boolean
    title?: string
    icon?: string

    hiddenInMenu?: boolean
    hidden?: boolean
  }
}

const routes: RouteRecordRaw[] = [
  {
    path: '/login',
    name: 'Login',
    component: () => import('@/views/auth/Login.vue'),
    meta: { requiresAuth: false },
  },
  {
    path: '/',
    component: () => import('@/views/layout/MainLayout.vue'),
    meta: { requiresAuth: true },
    children: [
      {
        path: '',
        redirect: '/dashboard',
      },
      {
        path: 'dashboard',
        name: 'Dashboard',
        component: () => import('@/views/dashboard/Dashboard.vue'),
        meta: { title: '仪表盘', icon: 'Odometer' },
      },
      {
        path: 'node',
        name: 'NodeList',
        component: loadNodeList,
        meta: { title: '节点', icon: 'Connection' },
      },
      {
        path: 'node/:id',
        name: 'NodeDetail',
        component: () => import('@/views/node/NodeOverview.vue'),
        meta: { title: '节点详情', hidden: true },
      },
      {
        path: 'node/:id/overview',
        name: 'NodeOverview',
        component: () => import('@/views/node/NodeOverview.vue'),
        meta: { title: '节点总览', hidden: true },
      },
      {
        path: 'channel',
        name: 'ChannelList',
        component: () => import('@/views/channel/ChannelList.vue'),
        meta: { title: '通道管理', icon: 'Connection' },
      },
      {
        path: 'edge-device',
        name: 'EdgeDeviceList',
        component: loadEdgeDeviceList,
        meta: { title: '边缘设备', icon: 'Cpu' },
      },
      {
        path: 'edge-device/:id',
        name: 'EdgeDeviceDetail',
        component: () => import('@/views/edge-device/EdgeDeviceDetailRouter.vue'),
        meta: { title: '边缘设备详情', hidden: true },
      },
      {
        path: 'logical-device',
        name: 'LogicalDeviceList',
        component: () => import('@/views/logical-device/LogicalDeviceList.vue'),
        meta: { title: '逻辑设备', icon: 'Share' },
      },
      {
        path: 'data-sources',
        name: 'DataSourceList',
        component: () => import('@/views/data-source/DataSourceList.vue'),
        meta: { title: '数据源', icon: 'Link' },
      },
      {
        path: 'data',
        name: 'DataPanel',
        component: () => import('@/views/data/DataPanel.vue'),
        meta: { title: '数据面板', icon: 'DataLine' },
      },
      {
        path: 'firmware',
        name: 'FirmwareManage',
        component: () => import('@/views/firmware/FirmwareManage.vue'),
        meta: { title: '固件管理', icon: 'Files' },
      },
      {
        path: 'device-configs',
        name: 'DeviceConfigList',
        component: () => import('@/views/config/DeviceConfigList.vue'),
        meta: { title: '配置模板', icon: 'Setting' },
      },
      {
        path: 'monitor',
        name: 'Monitor',
        component: () => import('@/views/monitor/Monitor.vue'),
        meta: { title: '系统监控', icon: 'DataAnalysis' },
      },
      {
        path: 'alerts',
        name: 'AlertRules',
        component: () => import('@/views/alert/AlertRules.vue'),
        meta: { title: '告警规则', icon: 'Bell' },
      },
      {
        path: 'automation',
        name: 'AutomationRules',
        component: () => import('@/views/automation/AutomationRules.vue'),
        meta: { title: '自动化策略', icon: 'SetUp' },
      },
      {
        // 外发通知通道：列表 + 分页 + 删除。
        path: 'notification-channels',
        name: 'NotificationChannels',
        component: () => import('@/views/notification/NotificationChannels.vue'),
        meta: { title: '通知通道', icon: 'Bell' },
      },
      {
        // 投递审计（P2-A）：只读列表 + channel_id/state 过滤 + 真分页，与通知通道同域相邻。
        path: 'notification-deliveries',
        name: 'NotificationDeliveries',
        component: () => import('@/views/notification/NotificationDeliveries.vue'),
        meta: { title: '投递审计', icon: 'Tickets' },
      },
      {
        path: 'profile',
        name: 'Profile',
        component: () => import('@/views/profile/Profile.vue'),
        meta: { title: '个人设置', icon: 'Setting', hiddenInMenu: true },
      },
    ],
  },
  // 403 无权限
  {
    path: '/403',
    name: 'Forbidden',
    component: () => import('@/views/error/Forbidden.vue'),
    meta: { requiresAuth: false },
  },
  // 离线 UI 验证：注入模拟数据渲染 BMS 指标区，无需后端/登录（仅开发环境）。
  // DEV 门禁：生产构建时 import.meta.env.DEV 为 false，路由与懒加载 chunk 均被
  // Vite 静态消除——MockBmsPanel 对 edgeDeviceApi 的 mock 补丁不可能进入生产。
  ...(import.meta.env.DEV ? [{
    path: '/dev/mock-bms',
    name: 'MockBmsPanel',
    component: () => import('@/dev/MockBmsPanel.vue'),
    meta: { requiresAuth: false, hidden: true },
  }, {
    // BMS 详情页设计稿像素级预览（designs/bms.png 复刻，静态 mock 数据，带 DEV 水印）。
    path: '/dev/bms-demo',
    name: 'BmsDemoPage',
    component: () => import('@/dev/BmsDemoPage.vue'),
    meta: { requiresAuth: false, hidden: true },
  }] : []),
  // 404 兜底（必须放最后）
  {
    path: '/:pathMatch(.*)*',
    name: 'NotFound',
    component: () => import('@/views/error/NotFound.vue'),
    meta: { requiresAuth: false },
  },
]

const router = createRouter({
  // 子路径前缀部署（反代 /ehome-dev 场景）：history base 跟随 VITE_BASE_PATH
  history: createWebHistory(import.meta.env.VITE_BASE_PATH || '/'),
  routes,
})

// 路由守卫
router.beforeEach((to, _from) => {
  const userStore = useUserStore()
  const wsStore = useWebSocketStore()
  // 进度反馈：懒加载 chunk 下载/路由解析耗时无法预测，开始导航即唤起顶部进度条
  useRouteProgress().start()

  // 1. 认证检查。
  // ⚠️ isLoggedIn 不能只看「token 字符串存在」：浏览器里残留的旧 token 同样是
  // 非空字符串，但服务端早已拒绝它（WS 握手 401 不经过 axios 拦截器，见
  // stores/websocket.ts 的 B1）。若沿用「存在即已登录」，被踢回 /login 后
  // 守卫会立刻把用户弹回 /dashboard，形成「登录 → 401 → 回登录页 → 又被弹回」
  // 的往复；且在 WS 永久 401 时用户没有任何自愈路径。
  // 判定收敛为：token 存在 **且** 未被权威判定失效。
  const sessionInvalid = wsStore.isCurrentTokenInvalidated()
  const authenticated = userStore.isLoggedIn && !sessionInvalid
  if (to.meta.requiresAuth && !authenticated) {
    return { path: '/login', query: { redirect: to.fullPath } }
  }
  // 仅「确实未失效」时才把 /login 弹回首页；失效态必须放过，否则用户到不了登录页
  if (to.path === '/login' && authenticated) {
    return '/dashboard'
  }
})

// 导航完成/失败时收敛进度条（收满后淡出）。
// 注意 afterEach 在重定向链最后一段成功后触发（而非每次重定向），
// 与 beforeEach 的 start() 防重入配合，避免进度条过早消失。
router.afterEach(() => {
  useRouteProgress().done()
})

// 导航错误（如懒加载 chunk 加载失败）：同样收敛，避免进度条卡死。
// 失败路径的视觉收敛与成功一致，由组件层（登录过渡层）负责补充错误提示。
router.onError(() => {
  useRouteProgress().fail()
})

export default router
