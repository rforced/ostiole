import { createRouter, createWebHistory } from 'vue-router'

import { useAuthStore } from '@/stores/auth'
import { useSystemStore } from '@/stores/system'
import DashboardView from '@/views/DashboardView.vue'

/** Session-scoped flag so "Skip for now" is honoured until the tab closes. */
const SKIP_WIZARD_KEY = 'ostiole.skipWizard'

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
    { path: '/wizard', name: 'wizard', component: () => import('@/views/WizardView.vue') },
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

  // A configured-or-not check sends a fresh box to the wizard once.
  if (auth.loggedIn && !to.meta.public) {
    const system = useSystemStore()
    if (system.status === null) await system.refresh()
    const unconfigured = system.status !== null && !system.status.configured
    if (to.name === 'wizard' && !unconfigured) return { name: 'dashboard' }
    if (unconfigured && to.name !== 'wizard') {
      if (to.name === 'dashboard' && to.redirectedFrom?.name === 'wizard') {
        sessionStorage.setItem(SKIP_WIZARD_KEY, '1')
      }
      if (!sessionStorage.getItem(SKIP_WIZARD_KEY)) return { name: 'wizard' }
    }
  }
  return true
})

export default router
