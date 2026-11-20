<script setup lang="ts">
import { toneOf } from "./tone";
import { onMounted, ref } from "vue";
import { api } from "../../types/api";
import {
  readinessLabel,
  type IdentityGroup,
  type ReviewIdentity,
} from "../../types/reviewIdentity";
import { useSession } from "../../stores/session";
import CliLoginDialog from "./CliLoginDialog.vue";

const emit = defineEmits<{ error: [string] }>();
const session = useSession();
const groups = ref<IdentityGroup[]>([]);
const loadError = ref(""),
  busy = ref("");
const login = ref<{ id: string; host: string } | null>(null);

async function load() {
  try {
    groups.value =
      (await api.get<IdentityGroup[]>("/api/v1/review-identities")) ?? [];
    loadError.value = "";
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e);
  }
}
async function pin(g: IdentityGroup, body: object, key: string) {
  busy.value = key;
  try {
    await api.put<ReviewIdentity>("/api/v1/review-identities", {
      provider: g.provider,
      host: g.host,
      ...body,
    });
    await load();
  } catch (e) {
    emit("error", e instanceof Error ? e.message : String(e));
  } finally {
    busy.value = "";
  }
}
async function startLogin(g: IdentityGroup) {
  busy.value = `login-${g.host}`;
  try {
    const res = await api.post<{ login_id: string }>(
      "/api/v1/review-identities/cli/login",
      { tool: "gh", host: g.host },
    );
    login.value = { id: res.login_id, host: g.host };
  } catch (e) {
    emit("error", e instanceof Error ? e.message : String(e));
  } finally {
    busy.value = "";
  }
}
const isPinnedConn = (g: IdentityGroup, id: string) =>
  g.pinned?.source === "connection" &&
  g.connections.find((c) => c.id === id)?.login === g.pinned.login;
const isPinnedWb = (g: IdentityGroup, id: string) =>
  g.pinned?.source === "workbench" && g.pinned.connection_id === id;
const pinnedWbLabel = (g: IdentityGroup) => {
  const name = g.workbench?.find((w) => w.id === g.pinned?.connection_id)?.name;
  return name ? `via ${name} (workbench)` : "via workbench (server removed)";
};
const isPinnedCli = (g: IdentityGroup, user: string) =>
  g.pinned?.source === "gh" && g.pinned.login === user;
onMounted(load);
</script>
<template>
  <div class="review-ids" data-test="review-identities">
    <h3 class="sub">
      Review identity
      <span class="count">Who posts when you review a pull request</span>
      <button class="btn small" type="button" @click="load">Refresh</button>
    </h3>
    <p v-if="loadError" class="help" data-test="identities-error">
      Review identities are unavailable: {{ loadError }}
    </p>
    <div class="card-list">
      <div
        v-for="g in groups"
        :key="`${g.provider}@${g.host}`"
        class="provider"
        :data-identity="`${g.provider}@${g.host}`"
      >
        <div class="agent-head">
          <span
            class="provider-logo"
            :data-tone="toneOf(g.provider)"
            aria-hidden="true"
            >{{ g.provider[0]?.toUpperCase() }}</span
          >
          <div class="grow">
            <h3>{{ g.provider }} · {{ g.host }}</h3>
            <p data-test="pinned">
              <template v-if="g.pinned"
                >Posting as <b>{{ g.pinned.login }}</b><template
                  v-if="g.pinned.source === 'workbench'"
                >
                  {{ " " + pinnedWbLabel(g) }}</template
                ></template
              >
              <template v-else>Not set</template>
            </p>
          </div>
        </div>
        <div
          v-for="c in g.connections"
          :key="c.id"
          class="acct"
          :data-connection-row="c.id"
        >
          <div class="grow">
            <b>{{ c.login || c.name }}</b>
            <span class="help">Token connection {{ c.name }}</span>
          </div>
          <span
            class="pill"
            :class="readinessLabel(c.readiness, c.expires_at).tone"
            >{{ readinessLabel(c.readiness, c.expires_at).text }}</span
          >
          <button
            v-if="session.canWrite"
            class="btn small"
            type="button"
            :disabled="!!busy || isPinnedConn(g, c.id)"
            @click="pin(g, { source: 'connection', connection_id: c.id }, c.id)"
          >
            {{ isPinnedConn(g, c.id) ? "In use" : "Use this" }}
          </button>
        </div>
        <div
          v-for="w in g.workbench"
          :key="w.id"
          class="acct"
          :data-workbench-row="w.id"
        >
          <div class="grow">
            <b>via {{ w.name }} (workbench)</b>
            <span class="help"
              >Uses your workbench Bitbucket sign-in, no API token needed</span
            >
          </div>
          <button
            v-if="session.canWrite"
            class="btn small"
            type="button"
            :disabled="!!busy || isPinnedWb(g, w.id)"
            @click="
              pin(g, { source: 'workbench', connection_id: w.id }, `wb-${w.id}`)
            "
          >
            {{ isPinnedWb(g, w.id) ? "In use" : "Use this" }}
          </button>
        </div>
        <template v-if="g.cli">
          <div class="acct" data-test="cli-row">
            <div class="grow">
              <b>GitHub CLI</b>
              <span class="help">{{
                g.cli.installed
                  ? g.cli.users.length
                    ? "Borrows the gh login on this machine"
                    : "gh is installed but not logged in"
                  : "gh is not installed"
              }}</span>
            </div>
            <button
              v-if="session.canWrite && g.cli.installed"
              class="btn small"
              type="button"
              :disabled="!!busy"
              @click="startLogin(g)"
            >
              Login with gh
            </button>
          </div>
          <div
            v-for="u in g.cli.users"
            :key="u"
            class="acct"
            :data-cli-user="u"
          >
            <div class="grow">
              <b>{{ u }}</b>
            </div>
            <button
              v-if="session.canWrite"
              class="btn small"
              type="button"
              :disabled="!!busy || isPinnedCli(g, u)"
              @click="pin(g, { source: 'gh', login: u }, `gh-${u}`)"
            >
              {{ isPinnedCli(g, u) ? "In use" : "Use this" }}
            </button>
          </div>
        </template>
        <p v-else-if="g.provider === 'gitlab'" class="help">
          GitLab reviews use a token connection. Signing in with glab is not
          supported.
        </p>
      </div>
      <p v-if="!groups.length && !loadError" class="help">
        Connect a code host above to choose a review identity.
      </p>
    </div>
    <CliLoginDialog
      v-if="login"
      :open="!!login"
      :login-id="login.id"
      :host="login.host"
      @close="login = null"
      @finished="load"
    />
  </div>
</template>
