import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import { useUserStore } from '@/stores/user'
import type { UserRole } from '@/stores/user'

declare module 'vue-router' {
  interface RouteMeta {
    requiresAuth?: boolean
    title?: string
    icon?: string
    roles?: UserRole[]
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
        path: 'collectors',
        name: 'CollectorList',
        component: () => import('@/views/collector/CollectorList.vue'),
        meta: { title: '采集器', icon: 'Connection' },
      },
      {
        path: 'collectors/:id',
        name: 'CollectorDetail',
        component: () => import('@/views/collector/CollectorDetail.vue'),
        meta: { title: '采集器详情', hidden: true },
      },
      {
        path: 'devices',
        name: 'DeviceList',
        component: () => import('@/views/device/DeviceList.vue'),
        meta: { title: '设备', icon: 'Cpu' },
      },
      {
        path: 'devices/:id',
        name: 'DeviceDetail',
        component: () => import('@/views/device/DeviceDetail.vue'),
        meta: { title: '设备详情', hidden: true },
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
        meta: { title: '固件管理', icon: 'Files', roles: ['admin', 'operator'] },
      },
      {
        path: 'device-configs',
        name: 'DeviceConfigList',
        component: () => import('@/views/config/DeviceConfigList.vue'),
        meta: { title: '配置模板', icon: 'Setting', roles: ['admin', 'operator'] },
      },
      {
        path: 'monitor',
        name: 'Monitor',
        component: () => import('@/views/monitor/Monitor.vue'),
        meta: { title: '系统监控', icon: 'DataAnalysis', roles: ['admin'] },
      },
      {
        path: 'admin/users',
        name: 'UserList',
        component: () => import('@/views/admin/UserList.vue'),
        meta: { title: '用户管理', icon: 'UserFilled', roles: ['admin'], hiddenInMenu: true },
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

  // 1. 认证检查
  if (to.meta.requiresAuth && !userStore.isLoggedIn) {
    return { path: '/login', query: { redirect: to.fullPath } }
  }
  if (to.path === '/login' && userStore.isLoggedIn) {
    return '/dashboard'
  }

  // 2. 角色检查
  const requiredRoles = to.meta.roles as UserRole[] | undefined
  if (requiredRoles && requiredRoles.length > 0) {
    const userRole = userStore.role
    if (!requiredRoles.includes(userRole)) {
      // 无权限 → 跳 403
      return { path: '/403', replace: true }
    }
  }
})

export default router
