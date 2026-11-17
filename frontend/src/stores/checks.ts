import { defineStore } from "pinia";
import {
  api,
  type CheckDryRun,
  type CheckProfile,
  type CheckStep,
  type ChecksStatus,
  type SmokeProcess,
} from "../types/api";

const POLL_MS = 2000;

// The dry-run result for a proposed step, unless its command was edited after the run.
export function dryRunOf(
  results: CheckDryRun[] | undefined,
  kind: "setup" | "check",
  step: CheckStep,
): CheckDryRun | undefined {
  const r = results?.find((x) => x.kind === kind && x.name === step.name);
  return r && r.command === step.command ? r : undefined;
}

// A check that fails on the default branch before any change is made to it.
export function failsOnMain(
  results: CheckDryRun[] | undefined,
  step: CheckStep,
): boolean {
  return dryRunOf(results, "check", step)?.outcome === "FAIL";
}

// KEY=VALUE per line, the editable form of an env or params map.
export function mapToText(m: Record<string, string> | undefined): string {
  return Object.entries(m ?? {})
    .map(([k, v]) => `${k}=${v}`)
    .join("\n");
}

export function textToMap(t: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of t.split("\n")) {
    const i = line.indexOf("=");
    const k = (i < 0 ? line : line.slice(0, i)).trim();
    if (k) out[k] = i < 0 ? "" : line.slice(i + 1);
  }
  return out;
}

const newProcess = (name: string): SmokeProcess => ({
  name,
  command: "",
  port_env: "PORT",
  ready: "/",
});

export const useChecks = defineStore("checks", {
  state: () => ({
    projectID: "",
    status: null as ChecksStatus | null,
    draft: null as CheckProfile | null,
    draftOf: "",
    busy: false,
    error: "",
    timer: null as ReturnType<typeof setTimeout> | null,
  }),
  getters: {
    running: (s) => s.status?.run.status === "running",
    dirty: (s) =>
      !!s.draft &&
      !!s.status?.proposal &&
      JSON.stringify(s.draft) !== JSON.stringify(s.status.proposal.profile),
  },
  actions: {
    stop() {
      if (this.timer) clearTimeout(this.timer);
      this.timer = null;
    },
    reset() {
      this.stop();
      this.$reset();
    },
    async load(projectID: string) {
      if (projectID !== this.projectID) {
        this.reset();
        this.projectID = projectID;
      }
      try {
        const st = await api.get<ChecksStatus>(
          `/api/v1/projects/${projectID}/checks`,
        );
        if (projectID !== this.projectID) return;
        this.status = st;
        this.error = "";
        const stamp = st.proposal?.created_at ?? "";
        if (stamp !== this.draftOf) {
          this.draft = st.proposal
            ? structuredClone(st.proposal.profile)
            : null;
          this.draftOf = stamp;
        }
      } catch (e) {
        if (projectID === this.projectID) this.error = messageOf(e);
        return;
      }
      this.stop();
      if (this.running)
        this.timer = setTimeout(() => void this.load(projectID), POLL_MS);
    },
    async suggest(projectID: string) {
      this.busy = true;
      try {
        await api.post(`/api/v1/projects/${projectID}/checks/suggest`);
        this.error = "";
      } catch (e) {
        this.error = messageOf(e);
        return;
      } finally {
        this.busy = false;
      }
      await this.load(projectID);
    },
    setCommand(list: "setup" | "checks", index: number, command: string) {
      const step = this.draft?.[list]?.[index];
      if (step) step.command = command;
    },
    makeOptional(index: number) {
      const step = this.draft?.checks[index];
      if (step) step.optional = true;
    },
    setSmoke(on: boolean) {
      if (!this.draft) return;
      if (!on) this.draft.smoke = null;
      else if (!this.draft.smoke)
        this.draft.smoke = {
          start: [newProcess("web")],
          base: "web",
          routes_from: "",
          blocking: false,
        };
    },
    addProcess() {
      const sm = this.draft?.smoke;
      if (!sm) return;
      let n = sm.start.length + 1;
      while (sm.start.some((p) => p.name === `app${n}`)) n++;
      sm.start.push(newProcess(`app${n}`));
    },
    removeProcess(index: number) {
      const sm = this.draft?.smoke;
      if (!sm) return;
      const [gone] = sm.start.splice(index, 1);
      if (gone && sm.base === gone.name) sm.base = sm.start[0]?.name ?? "";
    },
    renameProcess(index: number, name: string) {
      const sm = this.draft?.smoke;
      const p = sm?.start[index];
      if (!sm || !p) return;
      if (sm.base === p.name) sm.base = name;
      p.name = name;
    },
    setProcessEnv(index: number, text: string) {
      const p = this.draft?.smoke?.start[index];
      if (p) p.env = textToMap(text);
    },
    setParams(text: string) {
      const sm = this.draft?.smoke;
      if (sm) sm.params = textToMap(text);
    },
    setLogin(field: "path" | "user_env" | "password_env", value: string) {
      const sm = this.draft?.smoke;
      if (!sm) return;
      const l = {
        path: "",
        user_env: "",
        password_env: "",
        ...sm.login,
        [field]: value,
      };
      sm.login = l.path || l.user_env || l.password_env ? l : undefined;
    },
    async approve() {
      if (!this.draft || !this.projectID) return;
      const smoke = this.draft.smoke;
      // a cleared number input binds "", which the backend cannot decode as a number
      if (smoke && typeof smoke.ready_timeout_sec !== "number")
        delete smoke.ready_timeout_sec;
      this.busy = true;
      try {
        await api.put(`/api/v1/projects/${this.projectID}/checks`, {
          profile: this.draft,
        });
        this.error = "";
      } catch (e) {
        this.error = messageOf(e);
        return;
      } finally {
        this.busy = false;
      }
      await this.load(this.projectID);
    },
    async discard() {
      if (!this.projectID) return;
      this.busy = true;
      try {
        await api.del(`/api/v1/projects/${this.projectID}/checks/proposal`);
        this.error = "";
      } catch (e) {
        this.error = messageOf(e);
        return;
      } finally {
        this.busy = false;
      }
      await this.load(this.projectID);
    },
  },
});

function messageOf(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}
