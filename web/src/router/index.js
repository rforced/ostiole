import { createRouter, createWebHistory } from 'vue-router'

import { NAV } from '@/lib/nav'
import { useAuthStore } from '@/stores/auth'
import { useSystemStore } from '@/stores/system'

/** Session-scoped flag so "Skip for now" is honoured until the tab closes. */
const SKIP_WIZARD_KEY = 'ostiole.skipWizard'

/**
 * Entry point for an item that has pages: the bare path goes to the first
 * page, and a link written for the old tab bar (`/firewall#aliases`) goes to
 * the page that replaced that tab, so bookmarks still land.
 *
 * @param {import('@/lib/nav').NavItem} item
 * @returns {(to: import('vue-router').RouteLocation) => {path: string}}
 */
function sectionEntry(item) {
  const paths = item.pages.map((p) => p.path)
  return (to) => {
    const want = to.hash.slice(1)
    return { path: `${item.to}/${paths.includes(want) ? want : paths[0]}` }
  }
}

/** Pages that became tabs of another page; their old links still land. */
const MOVED = [
  { path: '/services/dhcp6', redirect: { path: '/services/dhcp', hash: '#v6' } },
  { path: '/services/leases', redirect: { path: '/services/dhcp', hash: '#leases' } },
]

/**
 * The route for a sidebar item: the one page it is, or the section layout
 * with its pages as children. Titles and tabs travel in the route meta so the
 * layout can head a page and the page can find its tabs without either
 * repeating the nav tree.
 *
 * @param {import('@/lib/nav').NavItem} item
 * @returns {import('vue-router').RouteRecordRaw}
 */
function itemRoute(item) {
  const name = item.to === '/' ? 'dashboard' : item.to.slice(1)
  if (!item.pages) {
    return { path: item.to, name, component: item.view, meta: { tabs: item.tabs } }
  }
  return {
    path: item.to,
    component: () => import('@/views/SectionView.vue'),
    meta: { section: item },
    children: [
      { path: '', redirect: sectionEntry(item) },
      ...item.pages.map((p) => ({
        path: p.path,
        name: `${name}-${p.path}`,
        component: p.view,
        meta: {
          title: p.label,
          tabs: p.tabs,
          needsConfig: p.needsConfig ?? item.needsConfig ?? false,
        },
      })),
    ],
  }
}

const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes: [
    ...NAV.map(itemRoute),
    ...MOVED,
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
