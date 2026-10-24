import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { createRouter, createMemoryHistory } from "vue-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import CatalogerView from "../../src/features/knowledge/CatalogerView.vue";
import { useFactory } from "../../src/stores/factory";
import type { KnowledgeRecord } from "../../src/types/api";

const sha = "0123456789abcdef0123456789abcdef01234567";
const records: KnowledgeRecord[] = [
  {
    id: "kn_1",
    workspace_id: "ws_default",
    project_id: "prj_1",
    work_order_id: "wo_1",
    repo_url: "/repos/greeter",
    commit_sha: sha,
    kind: "pr_decision",
    trust: "human_decision",
    title: 'PR approved: "Greeter"',
    body: "Reason: evidence complete\nPR reference: local:wo_1@" + sha,
    actor: "human",
    source_refs: ["pr:local:wo_1@" + sha],
    created_at: "2026-10-24T10:00:00Z",
  },
  {
    id: "kn_2",
    workspace_id: "ws_default",
    project_id: "prj_1",
    work_order_id: "wo_1",
    kind: "summary",
    trust: "ai_interpretation",
    title: 'Summary of "Greeter" (AI-generated)',
    body: "Scripted summary.",
    actor: "cataloger",
    source_refs: ["knowledge:kn_1", "run:run_9"],
    created_at: "2026-10-24T10:01:00Z",
  },
];

let fetchMock: ReturnType<typeof vi.fn>;
beforeEach(() => {
  setActivePinia(createPinia());
  const f = useFactory();
  f.projects = [
    {
      id: "prj_1",
      name: "Greeter",
      description: "",
      repo_url: "/repos/greeter",
      language: "go",
      created_at: "",
    },
  ];
  vi.spyOn(f, "refreshProjects").mockResolvedValue();
  fetchMock = vi.fn(
    async () => new Response(JSON.stringify({ records }), { status: 200 }),
  );
  vi.stubGlobal("fetch", fetchMock);
});
afterEach(() => vi.unstubAllGlobals());

async function render() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/knowledge", component: CatalogerView },
      { path: "/factory/:id", name: "work-order", component: CatalogerView },
    ],
  });
  await router.push("/knowledge");
  await router.isReady();
  const w = mount(CatalogerView, {
    global: { plugins: [router] },
    attachTo: document.body,
  });
  await flushPromises();
  return w;
}

it("lists stored records with their trust level, project and commit", async () => {
  const w = await render();
  expect(fetchMock).toHaveBeenCalledWith(
    "/api/v1/knowledge",
    expect.anything(),
  );
  const cards = w.findAll(".memory-card");
  expect(cards).toHaveLength(2);
  expect(cards[0].text()).toContain("Human decision");
  expect(cards[0].text()).toContain("Greeter");
  expect(cards[0].text()).toContain(sha.slice(0, 10));
  expect(cards[1].text()).toContain("AI interpretation");
  expect(cards[1].text()).toContain("unversioned");
  w.unmount();
});

it("sends project, trust and keyword filters to the API", async () => {
  const w = await render();
  await w.find("#kn-project").setValue("prj_1");
  await w.find("#kn-trust").setValue("verified");
  await w.find("#kn-search").setValue("  idempotency  ");
  await w.find('form[role="search"]').trigger("submit");
  await flushPromises();
  expect(fetchMock).toHaveBeenLastCalledWith(
    "/api/v1/knowledge?project_id=prj_1&trust=verified&q=idempotency",
    expect.anything(),
  );
  w.unmount();
});

it("shows provenance and flags AI interpretations as non-authoritative", async () => {
  const w = await render();
  await w.findAll(".memory-card")[1].trigger("click");
  await flushPromises();
  const dialog = document.body.textContent ?? "";
  expect(dialog).toContain("Not authoritative");
  expect(dialog).toContain("knowledge:kn_1");
  expect(dialog).toContain("run:run_9");
  expect(dialog).toContain("wo_1");
  w.unmount();
});

it("reports API failures instead of showing an empty knowledge base", async () => {
  fetchMock.mockResolvedValueOnce(
    new Response(
      JSON.stringify({
        error: { code: "NOT_FOUND", message: "project not found" },
      }),
      { status: 404 },
    ),
  );
  const w = await render();
  expect(w.find('[role="alert"]').text()).toContain("project not found");
  expect(w.find(".empty").exists()).toBe(false);
  w.unmount();
});
