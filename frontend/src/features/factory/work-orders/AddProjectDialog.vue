<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, reactive, ref, watch } from "vue";
import {
  Check,
  Copy,
  FolderGit2,
  FolderOpen,
  Globe,
  LoaderCircle,
  Laptop,
  Plus,
  TriangleAlert,
} from "lucide-vue-next";
import "../factory.css";
import "./add-project.css";
import AppDialog from "../../../components/ui/AppDialog.vue";
import Popover from "../../../components/ui/Popover.vue";
import { api, ApiError } from "../../../services/api/client";
import { useFactory } from "../../../stores/factory";
import { useSession } from "../../../stores/session";
import type { LocalGh, LocalInspect } from "../../../types/api";
const props = defineProps<{ open: boolean }>();
const emit = defineEmits<{ close: []; created: [string] }>();
type Step =
  | "probing"
  | "home"
  | "waiting"
  | "paste"
  | "clone"
  | "create"
  | "working"
  | "confirm"
  | "manual";
type Intent = "browse" | "clone" | "create";
const factory = useFactory(),
  session = useSession();
const step = ref<Step>("probing"),
  busy = ref(false),
  error = ref(""),
  gh = ref<LocalGh | null>(null),
  canPick = ref(true),
  info = ref<LocalInspect | null>(null),
  selected = ref<string[]>([]),
  pasted = ref(""),
  copied = ref(false),
  helpHost = ref(false),
  helpPublish = ref(false),
  createdID = ref("");
const form = reactive({
  name: "",
  repo_url: "",
  git_connection_id: "",
  remote_repo: "",
});
const clone = reactive({ url: "", parent: "" });
const fresh = reactive({ name: "", parent: "" });
const browseBtn = ref<HTMLButtonElement>();
let controller: AbortController | null = null;
let intent: Intent = "browse";
const gitConnections = computed(() =>
  factory.connections.filter(
    (c) =>
      c.scope === "shared" && c.status === "active" && c.provider !== "jira",
  ),
);
const norm = (p: string) => p.replace(/\/+$/, "");
const used = (path: string) =>
  factory.projects.some((p) => norm(p.repo_url) === norm(path));
const kind = computed(() =>
  !info.value
    ? "none"
    : info.value.is_git
      ? "repo"
      : info.value.repos?.length
        ? "many"
        : "plain",
);
const ghReady = computed(() => !!gh.value?.installed && gh.value.authenticated);
const needsGh = computed(() =>
  kind.value === "many"
    ? !!info.value?.repos.some((r) => r.github_repo)
    : !!info.value?.github_repo,
);
const fixCommand = computed(() =>
  gh.value?.installed ? "gh auth login" : "brew install gh && gh auth login",
);
const addable = computed(() =>
  kind.value === "many"
    ? selected.value.length
    : kind.value === "repo" && info.value && !used(info.value.path)
      ? 1
      : 0,
);
const title = computed(() =>
  step.value === "clone"
    ? "Clone from URL"
    : step.value === "create"
      ? "Create new project"
      : "Add a project",
);
const message = (e: unknown) => (e instanceof Error ? e.message : String(e));
function reset() {
  controller?.abort();
  controller = null;
  step.value = "probing";
  busy.value = false;
  error.value = "";
  info.value = null;
  selected.value = [];
  pasted.value = "";
  createdID.value = "";
  Object.assign(form, {
    name: "",
    repo_url: "",
    git_connection_id: "",
    remote_repo: "",
  });
  Object.assign(clone, { url: "", parent: "" });
  Object.assign(fresh, { name: "", parent: "" });
}
async function probe() {
  factory.refreshConnections().catch(() => {});
  try {
    gh.value = await api.get<LocalGh>("/api/v1/local/gh");
    step.value = "home";
    await nextTick();
    setTimeout(() => browseBtn.value?.focus());
  } catch {
    step.value = "manual";
  }
}
function enter(e: KeyboardEvent) {
  if (e.key !== "Enter" || step.value !== "home") return;
  const el = document.activeElement as HTMLElement | null;
  if (el?.closest("button,a,input,select,textarea,summary")) return;
  e.preventDefault();
  void browse();
}
watch(
  () => props.open,
  (open) => {
    document.removeEventListener("keydown", enter);
    if (!open) {
      controller?.abort();
      return;
    }
    reset();
    document.addEventListener("keydown", enter);
    void probe();
  },
  { immediate: true },
);
onBeforeUnmount(() => {
  document.removeEventListener("keydown", enter);
  controller?.abort();
});
async function choose(target: Intent): Promise<string | null> {
  intent = target;
  error.value = "";
  const before = step.value;
  step.value = "waiting";
  controller = new AbortController();
  try {
    const res = await api.post<{ path?: string; cancelled?: boolean }>(
      "/api/v1/local/pick-folder",
      {},
      controller.signal,
    );
    step.value = before;
    return res.cancelled || !res.path ? null : res.path;
  } catch (e) {
    if (e instanceof ApiError && e.status === 501) {
      canPick.value = false;
      step.value = target === "browse" ? "paste" : before;
    } else {
      if (!(e instanceof DOMException && e.name === "AbortError"))
        error.value = message(e);
      step.value = before;
    }
    return null;
  } finally {
    controller = null;
  }
}
function cancelWait() {
  controller?.abort();
  step.value = intent === "browse" ? "home" : intent;
}
async function browse() {
  if (!canPick.value) {
    step.value = "paste";
    return;
  }
  const path = await choose("browse");
  if (path) await inspect(path);
}
function show(result: LocalInspect) {
  info.value = result;
  form.name = result.name;
  selected.value = (result.repos ?? [])
    .filter((r) => !used(r.path))
    .map((r) => r.path);
  step.value = "confirm";
}
async function inspect(path: string) {
  step.value = "working";
  error.value = "";
  try {
    show(
      await api.get<LocalInspect>(
        `/api/v1/local/inspect?path=${encodeURIComponent(path)}`,
      ),
    );
  } catch (e) {
    error.value = message(e);
    step.value = "paste";
  }
}
async function pickParent(into: { parent: string }, target: Intent) {
  const path = await choose(target);
  if (path) into.parent = path;
}
async function runLocal(path: string, payload: Record<string, string>) {
  if (busy.value) return;
  error.value = "";
  busy.value = true;
  const before = step.value;
  step.value = "working";
  try {
    show(await api.post<LocalInspect>(path, payload));
  } catch (e) {
    error.value = message(e);
    step.value = before;
  } finally {
    busy.value = false;
  }
}
function submitClone() {
  if (!clone.url.trim() || !clone.parent.trim()) {
    error.value = "Repository URL and parent folder are required.";
    return;
  }
  void runLocal("/api/v1/local/clone", {
    url: clone.url.trim(),
    parent_dir: clone.parent.trim(),
  });
}
function submitCreate() {
  if (!fresh.name.trim() || !fresh.parent.trim()) {
    error.value = "Project name and parent folder are required.";
    return;
  }
  void runLocal("/api/v1/local/init", {
    name: fresh.name.trim(),
    parent_dir: fresh.parent.trim(),
  });
}
function toggleAll(on: boolean) {
  selected.value = on
    ? (info.value?.repos ?? []).filter((r) => !used(r.path)).map((r) => r.path)
    : [];
}
async function copyFix() {
  try {
    await navigator.clipboard.writeText(fixCommand.value);
    copied.value = true;
    setTimeout(() => (copied.value = false), 1500);
  } catch {
    /* Clipboard can be blocked; the command stays visible to select. */
  }
}
async function submit() {
  if (busy.value || !session.canWrite) return;
  if (createdID.value) {
    emit("created", createdID.value);
    return;
  }
  error.value = "";
  const manual = step.value === "manual";
  if (manual) {
    if (!form.name.trim() || !form.repo_url.trim()) {
      error.value = "Project name and repository are required.";
      return;
    }
    if (
      form.git_connection_id &&
      !/^[\w.-]+(\/[\w.-]+)+$/.test(form.remote_repo.trim())
    ) {
      error.value = "Remote repository must look like owner/repo.";
      return;
    }
  }
  busy.value = true;
  try {
    if (kind.value === "many" && !manual) {
      let first = "";
      for (const r of info.value!.repos.filter((r) =>
        selected.value.includes(r.path),
      )) {
        const p = await factory.createProject({
          name: r.name,
          repo_url: r.path,
        });
        first ||= p.id;
        selected.value = selected.value.filter((s) => s !== r.path);
      }
      emit("created", first);
      return;
    }
    const p = await factory.createProject({
      name: form.name.trim(),
      repo_url: manual ? form.repo_url.trim() : info.value!.path,
    });
    if (form.git_connection_id) {
      try {
        await factory.bindProject(p.id, {
          git_connection_id: form.git_connection_id,
          remote_repo: form.remote_repo.trim(),
        });
      } catch (e) {
        createdID.value = p.id;
        error.value = `Project created without a repository connection: ${message(e)}`;
        return;
      }
    }
    emit("created", p.id);
  } catch (e) {
    error.value = message(e);
  } finally {
    busy.value = false;
  }
}
const primaryText = computed(() => {
  if (step.value === "paste") return "Continue";
  if (step.value === "clone") return "Clone";
  if (step.value === "create") return "Create";
  if (busy.value) return "Adding…";
  if (createdID.value) return "Close";
  if (step.value === "manual") return "Create project";
  if (kind.value === "many")
    return `Add ${selected.value.length} project${selected.value.length === 1 ? "" : "s"}`;
  return "Add project";
});
const showFooter = computed(
  () => !["probing", "home", "waiting", "working"].includes(step.value),
);
const primaryDisabled = computed(
  () =>
    busy.value ||
    (step.value === "paste" && !pasted.value.trim()) ||
    (step.value === "clone" && !clone.url.trim()) ||
    (step.value === "create" && !fresh.name.trim()) ||
    (["confirm", "manual"].includes(step.value) &&
      (!session.canWrite ||
        (!createdID.value && step.value === "confirm" && !addable.value))),
);
function primary() {
  if (step.value === "paste") return void inspect(pasted.value.trim());
  if (step.value === "clone") return submitClone();
  if (step.value === "create") return submitCreate();
  return void submit();
}
function back() {
  error.value = "";
  info.value = null;
  step.value = "home";
}
function close() {
  if (!busy.value) emit("close");
}
</script>
<template>
  <AppDialog :open="open" :title="title" @close="close">
    <div class="ap">
      <div v-if="step === 'probing'" class="ap-wait" aria-busy="true">
        <LoaderCircle class="icon ap-spin" />
      </div>

      <template v-else-if="step === 'home'">
        <div class="ap-host">
          Host
          <span class="ap-chip"><Laptop class="icon" />This computer</span>
          <Popover v-model:open="helpHost">
            <template #trigger="{ toggle }"
              ><button
                class="fx-help"
                type="button"
                aria-label="What is the host?"
                aria-haspopup="true"
                :aria-expanded="helpHost"
                @click="toggle"
              >
                ?
              </button></template
            >
            <div class="fx-explain" @click.stop>
              <p class="small muted">
                Projects live on the computer running Legos, so agents work on
                your local files. Other hosts are not available yet.
              </p>
            </div>
          </Popover>
        </div>
        <button
          ref="browseBtn"
          class="ap-primary"
          type="button"
          @click="browse"
        >
          <span class="ap-row-icon"><FolderOpen class="icon" /></span>
          <span class="ap-row-text"
            ><b>Browse folder</b
            ><small
              >Local project, Git repo, or folder with many repos</small
            ></span
          >
          <kbd aria-hidden="true">⏎</kbd>
        </button>
        <div class="ap-others">
          <div class="ap-label">Other ways to add</div>
          <button class="ap-other" type="button" @click="step = 'clone'">
            <span class="ap-row-icon"><Globe class="icon" /></span>
            <span class="ap-row-text"
              ><b>Clone from URL</b
              ><small>Clone a remote Git repository</small></span
            >
          </button>
          <button class="ap-other" type="button" @click="step = 'create'">
            <span class="ap-row-icon"><Plus class="icon" /></span>
            <span class="ap-row-text"
              ><b>Create new project</b
              ><small>Start from an empty folder</small></span
            >
          </button>
        </div>
      </template>

      <div v-else-if="step === 'waiting'" class="ap-wait" role="status">
        <LoaderCircle class="icon ap-spin" />
        <b>Choose a folder in the window that opened…</b>
        <button class="lg-btn" type="button" @click="cancelWait">Cancel</button>
      </div>

      <div v-else-if="step === 'working'" class="ap-wait" aria-busy="true">
        <LoaderCircle class="icon ap-spin" />
        <span class="muted">Working on it…</span>
      </div>

      <form v-else-if="step === 'paste'" class="ap" @submit.prevent="primary">
        <div class="form-group">
          <label for="ap-path">Paste a path</label>
          <input
            id="ap-path"
            v-model="pasted"
            placeholder="/Users/you/code/my-project"
          />
          <p class="help">
            This system cannot open a folder picker, so enter the folder's full
            path.
          </p>
        </div>
      </form>

      <form v-else-if="step === 'clone'" class="ap" @submit.prevent="primary">
        <div class="form-group">
          <label for="ap-url">Repository URL</label>
          <input
            id="ap-url"
            v-model="clone.url"
            placeholder="https://github.com/owner/repo.git"
          />
        </div>
        <div class="form-group">
          <label for="ap-parent">Parent folder</label>
          <div class="ap-inline">
            <input
              id="ap-parent"
              v-model="clone.parent"
              placeholder="/Users/you/code"
            />
            <button
              v-if="canPick"
              class="lg-btn"
              type="button"
              @click="pickParent(clone, 'clone')"
            >
              <FolderOpen class="icon" />Browse
            </button>
          </div>
        </div>
      </form>

      <form v-else-if="step === 'create'" class="ap" @submit.prevent="primary">
        <div class="form-group">
          <label for="ap-new-name">Project name</label>
          <input
            id="ap-new-name"
            v-model="fresh.name"
            placeholder="payments-platform"
          />
        </div>
        <div class="form-group">
          <label for="ap-new-parent">Parent folder</label>
          <div class="ap-inline">
            <input
              id="ap-new-parent"
              v-model="fresh.parent"
              placeholder="/Users/you/code"
            />
            <button
              v-if="canPick"
              class="lg-btn"
              type="button"
              @click="pickParent(fresh, 'create')"
            >
              <FolderOpen class="icon" />Browse
            </button>
          </div>
        </div>
      </form>

      <form
        v-else-if="step === 'manual'"
        id="project-form"
        class="ap"
        @submit.prevent="primary"
      >
        <div class="form-group">
          <label for="prj-name">Project name</label>
          <input
            id="prj-name"
            v-model="form.name"
            required
            placeholder="Payments platform"
          />
        </div>
        <div class="form-group">
          <label for="prj-repo">Repository (local path or URL)</label>
          <input
            id="prj-repo"
            v-model="form.repo_url"
            required
            placeholder="/path/to/repo or https://…"
          />
        </div>
        <div class="form-group">
          <label for="prj-conn">Pull requests (optional)</label>
          <select id="prj-conn" v-model="form.git_connection_id">
            <option value="">Local only · no pull requests</option>
            <option v-for="c in gitConnections" :key="c.id" :value="c.id">
              {{ c.name }} · {{ c.provider }}
            </option>
          </select>
          <p class="help">
            Shared connections only. After a human approves a work order, its
            verified commit is pushed to a branch and a pull request is opened.
          </p>
        </div>
        <div v-if="form.git_connection_id" class="form-group">
          <label for="prj-remote">Remote repository</label>
          <input
            id="prj-remote"
            v-model="form.remote_repo"
            required
            placeholder="owner/repo"
          />
          <p class="help">
            The repository above must be its clone URL, e.g.
            https://github.com/owner/repo.git.
          </p>
        </div>
      </form>

      <template v-else-if="step === 'confirm' && info">
        <div v-if="kind === 'plain'" class="ap-publish warn" role="status">
          <TriangleAlert class="icon" />
          <span
            ><b>{{ info.name }}</b> is not a Git repository and has no repos
            inside it. Pick a Git repo, or use Create new project.</span
          >
        </div>

        <template v-else-if="kind === 'many'">
          <div class="ap-bulk">
            <span class="ap-label"
              >{{ info.repos.length }} repositories in {{ info.name }}</span
            >
            <span class="row">
              <button
                class="lg-btn small ghost"
                type="button"
                @click="toggleAll(true)"
              >
                Select all</button
              ><button
                class="lg-btn small ghost"
                type="button"
                @click="toggleAll(false)"
              >
                None
              </button>
            </span>
          </div>
          <div class="ap-repos">
            <label v-for="r in info.repos" :key="r.path" class="ap-repo">
              <input
                v-model="selected"
                type="checkbox"
                :value="r.path"
                :disabled="used(r.path)"
              />
              <FolderGit2 class="icon" />
              <span>{{ r.name }}</span>
              <span v-if="used(r.path)" class="ap-dup">Already added</span>
              <span v-else>{{ r.github_repo || "Local only" }}</span>
            </label>
          </div>
        </template>

        <template v-else>
          <div v-if="used(info.path)" class="ap-publish warn" role="status">
            <TriangleAlert class="icon" />
            <span><b class="ap-dup">Already added</b> as a project.</span>
          </div>
          <div class="form-group">
            <label for="ap-name">Project name</label>
            <input id="ap-name" v-model="form.name" />
          </div>
          <div class="ap-facts">
            <span class="mono">{{ info.path }}</span>
            <span
              >{{ info.language || "Language not detected" }}
              <template v-if="info.default_branch">
                · branch {{ info.default_branch }}</template
              ></span
            >
          </div>
        </template>

        <div
          v-if="kind === 'repo' || (kind === 'many' && needsGh)"
          class="ap-publish"
          :class="{ warn: needsGh && !ghReady }"
        >
          <Check v-if="!needsGh || ghReady" class="icon" />
          <TriangleAlert v-else class="icon" />
          <div>
            <template v-if="!needsGh"
              >Local only — no remote, pull requests are off</template
            >
            <template v-else-if="ghReady"
              >Publishing
              <b v-if="kind === 'repo'">{{ info.github_repo }}</b> via your gh
              login<template v-if="gh?.account">
                ({{ gh.account }})</template
              ></template
            >
            <template v-else>
              {{
                gh?.installed
                  ? "GitHub CLI is not signed in, so pull requests will fail. Run"
                  : "GitHub CLI is not installed, so pull requests will fail. Run"
              }}
              <br />
              <button class="ap-code" type="button" @click="copyFix">
                {{ fixCommand }}<Check v-if="copied" class="icon" /><Copy
                  v-else
                  class="icon"
                />
              </button>
            </template>
          </div>
          <Popover v-model:open="helpPublish">
            <template #trigger="{ toggle }"
              ><button
                class="fx-help"
                type="button"
                aria-label="How does publishing work?"
                aria-haspopup="true"
                :aria-expanded="helpPublish"
                @click="toggle"
              >
                ?
              </button></template
            >
            <div class="fx-explain" @click.stop>
              <p class="small muted">
                After you approve a work order, its verified commit is pushed to
                a branch and a pull request is opened using the GitHub CLI login
                on this computer. Without a remote, work stays local.
              </p>
            </div>
          </Popover>
        </div>

        <details
          v-if="kind === 'repo' && gitConnections.length && !used(info.path)"
        >
          <summary>Advanced</summary>
          <div class="ap">
            <div class="form-group">
              <label for="prj-conn">Use a Settings connection instead</label>
              <select id="prj-conn" v-model="form.git_connection_id">
                <option value="">None · use gh or stay local</option>
                <option v-for="c in gitConnections" :key="c.id" :value="c.id">
                  {{ c.name }} · {{ c.provider }}
                </option>
              </select>
            </div>
            <div v-if="form.git_connection_id" class="form-group">
              <label for="prj-remote">Remote repository</label>
              <input
                id="prj-remote"
                v-model="form.remote_repo"
                placeholder="owner/repo"
              />
            </div>
          </div>
        </details>
      </template>

      <p v-if="error" class="form-error" role="alert">{{ error }}</p>
    </div>
    <template v-if="showFooter" #footer>
      <button
        class="lg-btn"
        type="button"
        :disabled="busy"
        @click="step === 'manual' ? close() : back()"
      >
        {{ step === "manual" ? "Cancel" : "Back" }}
      </button>
      <button
        v-if="!(step === 'confirm' && kind === 'plain')"
        class="lg-btn primary"
        :type="step === 'manual' ? 'submit' : 'button'"
        :form="step === 'manual' ? 'project-form' : undefined"
        :disabled="primaryDisabled"
        @click="step === 'manual' ? undefined : primary()"
      >
        {{ primaryText }}
      </button>
    </template>
  </AppDialog>
</template>
