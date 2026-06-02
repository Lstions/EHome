import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import { useUserStore } from '@/stores/user'

const routes: RouteRecordRaw[] = [
  {
    path: '/login',
    name: 'Login',
    component: () => import('@/views/auth/Login.vue'),
    meta: { requiresAuth: false }
  },
  {
    path: '/',
    component: () => import('@/views/layout/MainLayout.vue'),
    meta: { requiresAuth: true },
    children: [
      {
        path: '',
        redirect: '/dashboard'
      },
      {
        path: 'dashboard',
        name: 'Dashboard',
        component: () => import('@/views/dashboard/Dashboard.vue')
      },
      {
        path: 'collectors',
        name: 'CollectorList',
        component: () => import('@/views/collector/CollectorList.vue')
      },
      {
        path: 'collectors/:id',
        name: 'CollectorDetail',
        component: () => import('@/views/collector/CollectorDetail.vue')
      },
      {
        path: 'devices',
        name: 'DeviceList',
        component: () => import('@/views/device/DeviceList.vue')
      },
      {
        path: 'devices/:id',
        name: 'DeviceDetail',
        component: () => import('@/views/device/DeviceDetail.vue')
      },
      {
        path: 'data',
        name: 'DataPanel',
        component: () => import('@/views/data/DataPanel.vue')
      },
      {
        path: 'firmware',
        name: 'FirmwareManage',
        component: () => import('@/views/firmware/FirmwareManage.vue')
      },
      {
        path: 'device-configs',
        name: 'DeviceConfigList',
        component: () => import('@/views/config/DeviceConfigList.vue')
      },
      {
        path: 'monitor',
        name: 'Monitor',
        component: () => import('@/views/monitor/Monitor.vue')
      },
      {
        path: 'vendors',
        name: 'VendorManage',
        component: () => import('@/views/vendor/VendorManage.vue')
      },
      {
        path: 'data-sources',
        name: 'DataSourceManage',
        component: () => import('@/views/data-source/DataSourceManage.vue')
      },
      {
        path: 'notifications',
        name: 'NotificationCenter',
        component: () => import('@/views/notification/NotificationCenter.vue')
      },

    ]
  }
]

const router = createRouter({
  history: createWebHistory(),
  routes
})

// 路由守卫 - 认证检查
router.beforeEach((to, _from) => {
  const userStore = useUserStore()

  if (to.meta.requiresAuth && !userStore.isLoggedIn) {
    return '/login'
  }
  if (to.path === '/login' && userStore.isLoggedIn) {
    return '/dashboard'
  }
})

export default router
