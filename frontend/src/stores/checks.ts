import { defineStore } from "pinia";
import {
  api,
  type CheckDryRun,
  type CheckProfile,
  type CheckStep,
  type ChecksStatus,
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
          this.draft = st.proposal ? structuredClone(st.proposal.profile) : null;
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
    async approve() {
      if (!this.draft || !this.projectID) return;
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
