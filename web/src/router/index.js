import { createRouter, createWebHistory } from 'vue-router'

import { useAuthStore } from '@/stores/auth'
import DashboardView from '@/views/DashboardView.vue'

const placeholder = (title) => ({
  component: () => import('@/views/PlaceholderView.vue'),
  props: { title },
})

const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes: [
    { path: '/', name: 'dashboard', component: DashboardView },
    { path: '/interfaces', name: 'interfaces', ...placeholder('Interfaces') },
    { path: '/firewall', name: 'firewall', ...placeholder('Firewall') },
    { path: '/routing', name: 'routing', ...placeholder('Routing') },
    { path: '/services', name: 'services', ...placeholder('Services') },
    { path: '/system', name: 'system', ...placeholder('System') },
    {
      path: '/login',
      name: 'login',
      component: () => import('@/views/LoginView.vue'),
      meta: { public: true },
    },
    {
      path: '/setup',
      name: 'setup',
      component: () => import('@/views/SetupView.vue'),
      meta: { public: true },
    },
    {
      path: '/:pathMatch(.*)*',
      name: 'not-found',
      component: () => import('@/views/NotFoundView.vue'),
    },
  ],
})

router.beforeEach(async (to) => {
  const auth = useAuthStore()
  await auth.bootstrap()
  if (auth.setupNeeded && to.name !== 'setup') return { name: 'setup' }
  if (!auth.setupNeeded && to.name === 'setup') return { name: 'login' }
  if (!to.meta.public && !auth.loggedIn) {
    return { name: 'login', query: to.fullPath === '/' ? {} : { redirect: to.fullPath } }
  }
  if (to.name === 'login' && auth.loggedIn) return { name: 'dashboard' }
  return true
})

export default router
