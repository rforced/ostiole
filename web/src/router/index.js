import { createRouter, createWebHistory } from 'vue-router'

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
      path: '/:pathMatch(.*)*',
      name: 'not-found',
      component: () => import('@/views/NotFoundView.vue'),
    },
  ],
})

export default router
