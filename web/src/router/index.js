import { createRouter, createWebHistory } from 'vue-router'

import { FIREWALL_PAGES, SERVICE_PAGES } from '@/lib/sections'
import { useAuthStore } from '@/stores/auth'
import { useSystemStore } from '@/stores/system'
import DashboardView from '@/views/DashboardView.vue'

/** Session-scoped flag so "Skip for now" is honoured until the tab closes. */
const SKIP_WIZARD_KEY = 'ostiole.skipWizard'

/**
 * Entry point for a section that has child pages: the bare path goes to the
 * first child, and a link written for the old tab bar (`/firewall#aliases`)
 * goes to the child that replaced that tab, so bookmarks still land.
 *
 * @param {string} base section path, e.g. "/firewall"
 * @param {string[]} children child segments in the order the sidebar lists them
 * @returns {(to: import('vue-router').RouteLocation) => {path: string}}
 */
function sectionEntry(base, children) {
  return (to) => {
    const want = to.hash.slice(1)
    return { path: `${base}/${children.includes(want) ? want : children[0]}` }
  }
}

const pagePath = (p) => p.path

/**
 * Turns a section page into its child route. The title travels in the route so
 * the section layout can head the page without every page repeating it.
 *
 * @param {string} section name prefix, e.g. "firewall"
 */
const childRoute = (section) => (p) => ({
  path: p.path,
  name: `${section}-${p.path}`,
  component: p.view,
  meta: { title: p.label },
})

const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes: [
    { path: '/', name: 'dashboard', component: DashboardView },
    {
      path: '/interfaces',
      name: 'interfaces',
      component: () => import('@/views/InterfacesView.vue'),
    },
    {
      path: '/firewall',
      component: () => import('@/views/FirewallView.vue'),
      children: [
        { path: '', redirect: sectionEntry('/firewall', FIREWALL_PAGES.map(pagePath)) },
        ...FIREWALL_PAGES.map(childRoute('firewall')),
      ],
    },
    { path: '/routing', name: 'routing', component: () => import('@/views/RoutingView.vue') },
    {
      path: '/services',
      component: () => import('@/views/ServicesView.vue'),
      children: [
        { path: '', redirect: sectionEntry('/services', SERVICE_PAGES.map(pagePath)) },
        ...SERVICE_PAGES.map(childRoute('services')),
      ],
    },
    { path: '/vpn', name: 'vpn', component: () => import('@/views/VpnView.vue') },
    { path: '/crons', name: 'crons', component: () => import('@/views/CronsView.vue') },
    {
      path: '/diagnostics',
      name: 'diagnostics',
      component: () => import('@/views/DiagnosticsView.vue'),
    },
    { path: '/system', name: 'system', component: () => import('@/views/SystemView.vue') },
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
