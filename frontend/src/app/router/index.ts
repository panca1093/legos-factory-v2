import { createRouter, createWebHistory } from 'vue-router'
import { useSession } from '../../stores/session'
import AppShell from '../layouts/AppShell.vue'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    {
      path: '/',
      component: AppShell,
      children: [
        { path: '', name: 'overview', component: () => import('../../features/overview/OverviewView.vue') },
        { path: 'factory', name: 'factory', component: () => import('../../features/factory/FactoryView.vue') },
        { path: 'factory/:id', name: 'work-order', component: () => import('../../features/factory/workorders/WorkOrderView.vue') },
        { path: 'knowledge', name: 'knowledge', component: () => import('../../features/knowledge/CatalogerView.vue') },
        { path: 'settings', name: 'settings', component: () => import('../../features/settings/SettingsView.vue') },
      ],
    },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

router.beforeEach(async (to) => {
  const session = useSession()
  if (session.loading) await session.bootstrap()
  if (!session.user && to.name !== 'login') {
    // stay on any view; AppShell shows sign-in prompt
    if (session.mode === 'oidc') return { name: 'login', query: { from: to.fullPath } }
  }
})
