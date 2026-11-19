import { createRouter, createWebHistory } from "vue-router";
import AppShell from "../layouts/AppShell.vue";
import { api, type WorkOrder } from "../../types/api";
import { legacyTabPath, woPath } from "../../features/projects/rail";

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    {
      path: "/",
      component: AppShell,
      children: [
        {
          path: "",
          name: "overview",
          component: () => import("../../features/overview/OverviewView.vue"),
        },
        {
          path: "chats/:id?",
          name: "chats",
          component: () => import("../../features/chats/ChatsView.vue"),
        },
        {
          path: "projects",
          name: "projects",
          component: () => import("../../features/projects/ProjectsView.vue"),
        },
        {
          path: "projects/:id",
          component: () =>
            import("../../features/projects/ProjectWorkspace.vue"),
          children: [
            {
              path: "",
              name: "project",
              component: () =>
                import("../../features/projects/FactoryOverview.vue"),
            },
            {
              path: "wo/:wo",
              name: "project-wo",
              component: () =>
                import("../../features/factory/work-orders/WorkOrderView.vue"),
            },
            {
              path: "pulls",
              name: "project-pulls",
              component: () =>
                import("../../features/projects/pulls/PullsView.vue"),
            },
            {
              path: "pulls/:number(\\d+)",
              name: "project-pull",
              component: () =>
                import("../../features/projects/pulls/PRView.vue"),
            },
            {
              path: "code/:folder?",
              name: "project-code",
              component: () =>
                import("../../features/projects/ProjectPlaceholder.vue"),
            },
          ],
        },
        {
          path: "reviews",
          name: "reviews",
          component: () => import("../../features/projects/ReviewsView.vue"),
        },
        {
          path: "factory",
          redirect: (to) => {
            const { project, ...query } = to.query;
            return typeof project === "string" && project
              ? { path: `/projects/${project}`, query }
              : { path: "/projects", query };
          },
        },
        {
          path: "factory/:wo",
          name: "legacy-work-order",
          component: { render: () => null },
          beforeEnter: async (to) => {
            const id = String(to.params.wo);
            try {
              const res = await api.get<{ work_order: WorkOrder }>(
                `/api/v1/work-orders/${encodeURIComponent(id)}`,
              );
              return {
                path: woPath(res.work_order.project_id, id),
                query: to.query,
                replace: true,
              };
            } catch {
              return { path: "/projects", replace: true };
            }
          },
        },
        {
          path: "knowledge",
          name: "knowledge",
          component: () => import("../../features/knowledge/KnowledgeView.vue"),
        },
        {
          path: "settings",
          name: "settings",
          component: () => import("../../features/settings/SettingsView.vue"),
        },
      ],
    },
    { path: "/:pathMatch(.*)*", redirect: "/" },
  ],
});

router.beforeEach((to) => {
  if (to.name !== "project" || !to.query.tab) return true;
  const target = legacyTabPath(String(to.params.id), to.query.tab);
  if (!target) return true;
  const query = { ...to.query };
  delete query.tab;
  return { path: target, query, replace: true };
});
