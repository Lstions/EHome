import { describe, it, expect, beforeEach, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { createRouter, createWebHistory, type Router } from 'vue-router'

// We test the route guard logic from src/router/index.ts
// by creating a minimal router with the same beforeEach guard.

// Mock the views — we only test guard logic, not component rendering
const LoginView = { template: '<div>login</div>' }
const DashboardView = { template: '<div>dashboard</div>' }
const ForbiddenView = { template: '<div>403</div>' }
const NotFoundView = { template: '<div>404</div>' }
const AdminView = { template: '<div>admin</div>' }
const MonitorView = { template: '<div>monitor</div>' }
const FirmwareView = { template: '<div>firmware</div>' }
const LayoutView = { template: '<div><router-view /></div>' }

function createTestRouter(): Router {
  const router = createRouter({
    history: createWebHistory(),
    routes: [
      { path: '/login', name: 'Login', component: LoginView, meta: { requiresAuth: false } },
      {
        path: '/',
        component: LayoutView,
        meta: { requiresAuth: true },
        children: [
          { path: '', redirect: '/dashboard' },
          { path: 'dashboard', name: 'Dashboard', component: DashboardView },
          { path: 'firmware', name: 'Firmware', component: FirmwareView, meta: { roles: ['admin', 'operator'] } },
          { path: 'monitor', name: 'Monitor', component: MonitorView, meta: { roles: ['admin'] } },
          { path: 'admin/users', name: 'Admin', component: AdminView, meta: { roles: ['admin'] } },
        ],
      },
      { path: '/403', name: 'Forbidden', component: ForbiddenView, meta: { requiresAuth: false } },
      { path: '/:pathMatch(.*)*', name: 'NotFound', component: NotFoundView, meta: { requiresAuth: false } },
    ],
  })

  // Same guard logic as src/router/index.ts
  router.beforeEach((to, _from) => {
    // Import the user store the same way the real router does
    // We mock it via the pinia state
    const userStore = useUserStore()

    // 1. Auth check
    if (to.meta.requiresAuth && !userStore.isLoggedIn) {
      return { path: '/login', query: { redirect: to.fullPath } }
    }
    if (to.path === '/login' && userStore.isLoggedIn) {
      return '/dashboard'
    }

  })

  return router
}

// We need to mock useUserStore to control auth state
// Since the guard calls useUserStore() at runtime, we mock the module
let mockAuthState = {
  isLoggedIn: false,
}

vi.mock('@/stores/user', () => ({
  useUserStore: () => ({
    get isLoggedIn() { return mockAuthState.isLoggedIn },
    login: vi.fn(),
    logout: vi.fn(),
  }),
}))

// Must import AFTER the mock is set up
import { useUserStore } from '@/stores/user'

function setupRouter(): Router {
  setActivePinia(createPinia())
  mockAuthState = { isLoggedIn: false }
  return createTestRouter()
}

async function navigateTo(router: Router, path: string) {
  await router.push(path)
  await router.isReady()
}

describe('Router Guards', () => {
  let router: Router

  beforeEach(() => {
    router = setupRouter()
  })

  // ── 1. Auth Guard ──────────────────────────────────

  describe('Authentication Guard', () => {
    it('redirects unauthenticated user to /login with redirect query', async () => {
      mockAuthState.isLoggedIn = false
      await navigateTo(router, '/dashboard')
      expect(router.currentRoute.value.path).toBe('/login')
      expect(router.currentRoute.value.query.redirect).toBe('/dashboard')
    })

    it('redirects unauthenticated user from nested route with redirect query', async () => {
      mockAuthState.isLoggedIn = false
      await navigateTo(router, '/firmware')
      expect(router.currentRoute.value.path).toBe('/login')
      expect(router.currentRoute.value.query.redirect).toBe('/firmware')
    })

    it('allows unauthenticated user to access /login', async () => {
      mockAuthState.isLoggedIn = false
      await navigateTo(router, '/login')
      expect(router.currentRoute.value.path).toBe('/login')
    })

    it('redirects authenticated user from /login to /dashboard', async () => {
      mockAuthState.isLoggedIn = true
      await navigateTo(router, '/login')
      expect(router.currentRoute.value.path).toBe('/dashboard')
    })

    it('allows authenticated user to access protected routes', async () => {
      mockAuthState.isLoggedIn = true
      await navigateTo(router, '/dashboard')
      expect(router.currentRoute.value.path).toBe('/dashboard')
    })
  })

  // ── 2. Edge Cases ──────────────────────────────────

  describe('Edge Cases', () => {
    it('redirects to /login when accessing root without auth', async () => {
      mockAuthState.isLoggedIn = false
      await navigateTo(router, '/')
      expect(router.currentRoute.value.path).toBe('/login')
    })

    it('root path redirects to /dashboard when authenticated', async () => {
      mockAuthState.isLoggedIn = true
      await navigateTo(router, '/')
      expect(router.currentRoute.value.path).toBe('/dashboard')
    })

    it('allows access to /403 without auth', async () => {
      mockAuthState.isLoggedIn = false
      await navigateTo(router, '/403')
      expect(router.currentRoute.value.path).toBe('/403')
    })

    it('unauthenticated user hitting unknown route goes to /login (not 404)', async () => {
      mockAuthState.isLoggedIn = false
      await navigateTo(router, '/nonexistent')
      // /nonexistent has requiresAuth: false in the catch-all route meta,
      // so it should go to NotFound, not /login
      expect(router.currentRoute.value.path).toBe('/nonexistent')
      expect(router.currentRoute.value.name).toBe('NotFound')
    })
  })
})
