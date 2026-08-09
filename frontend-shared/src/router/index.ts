import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import { useUserStore } from '@/stores/user'

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
        component: () => import('@/views/node/NodeDetail.vue'),
        meta: { title: '节点详情', hidden: true },
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
  // 离线 UI 验证：注入模拟数据渲染 BMS 指标区，无需后端/登录（仅开发/测试用）
  {
    path: '/dev/mock-bms',
    name: 'MockBmsPanel',
    component: () => import('@/dev/MockBmsPanel.vue'),
    meta: { requiresAuth: false, hidden: true },
  },
  // 404 兜底（必须放最后）
  {
    path: '/:pathMatch(.*)*',
    name: 'NotFound',
    component: () => import('@/views/error/NotFound.vue'),
    meta: { requiresAuth: false },
  },
]

const router = createRouter({
  history: createWebHistory(),
  routes,
})

// 路由守卫
router.beforeEach((to, _from) => {
  const userStore = useUserStore()
  // 进度反馈：懒加载 chunk 下载/路由解析耗时无法预测，开始导航即唤起顶部进度条
  useRouteProgress().start()

  // 1. 认证检查
  if (to.meta.requiresAuth && !userStore.isLoggedIn) {
    return { path: '/login', query: { redirect: to.fullPath } }
  }
  if (to.path === '/login' && userStore.isLoggedIn) {
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
