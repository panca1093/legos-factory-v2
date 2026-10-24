import { defineStore } from "pinia";
import { subscribe, type LiveEvent } from "../services/events/sse";
import {
  api,
  type Connection,
  type Finding,
  type IssueDraft,
  type ModelInfo,
  type Overview,
  type Project,
  type QualityCheck,
  type Run,
  type Settings,
  type Task,
  type WorkOrder,
  type WOEvent,
} from "../types/api";

export interface WODetail {
  work_order: WorkOrder;
  plan: Task[];
  runs: Run[];
}

export const useFactory = defineStore("factory", {
  state: () => ({
    projects: [] as Project[],
    workOrders: [] as WorkOrder[],
    overview: null as Overview | null,
    models: [] as ModelInfo[],
    connections: [] as Connection[],
    credentialsConfigured: true,
    detail: null as WODetail | null,
    events: [] as WOEvent[],
    quality: [] as QualityCheck[],
    findings: [] as Finding[],
    loading: false,
    error: "" as string,
    selectedID: "",
    workspaceEpoch: 0,
    requestID: 0,
    stopStream: null as (() => void) | null,
    liveEvents: [] as LiveEvent[],
    connected: false,
  }),
  actions: {
    resetWorkspace() {
      const requestID = this.requestID + 1;
      const epoch = this.workspaceEpoch + 1;
      this.stopLive();
      this.$reset();
      this.requestID = requestID;
      this.workspaceEpoch = epoch;
    },
    startLive() {
      if (this.stopStream) return;
      this.stopStream = subscribe(
        "/api/v1/events",
        (ev) => {
          this.connected = true;
          this.liveEvents = [ev, ...this.liveEvents].slice(0, 20);
          void this.refreshOverview().catch(() => {});
          void this.refreshWorkOrders().catch(() => {});
        },
        () => {
          this.connected = false;
        },
      );
    },
    stopLive() {
      this.stopStream?.();
      this.stopStream = null;
      this.connected = false;
    },
    clearDetail() {
      this.requestID++;
      this.selectedID = "";
      this.detail = null;
      this.events = [];
      this.quality = [];
      this.findings = [];
      this.loading = false;
      this.error = "";
    },
    async refreshProjects() {
      const epoch = this.workspaceEpoch;
      const res = await api.get<{ projects: Project[] }>("/api/v1/projects");
      if (epoch === this.workspaceEpoch) this.projects = res.projects ?? [];
    },
    async refreshWorkOrders() {
      const epoch = this.workspaceEpoch;
      const res = await api.get<{ work_orders: WorkOrder[] }>(
        "/api/v1/work-orders",
      );
      if (epoch === this.workspaceEpoch)
        this.workOrders = res.work_orders ?? [];
    },
    async refreshOverview() {
      const epoch = this.workspaceEpoch;
      const overview = await api.get<Overview>("/api/v1/overview");
      if (epoch === this.workspaceEpoch) this.overview = overview;
    },
    async refreshModels() {
      const epoch = this.workspaceEpoch;
      const res = await api.get<{ models: ModelInfo[] }>("/api/v1/models");
      if (epoch === this.workspaceEpoch) this.models = res.models ?? [];
    },
    async refreshConnections() {
      const epoch = this.workspaceEpoch;
      const res = await api.get<{
        connections: Connection[];
        credentials_configured: boolean;
      }>("/api/v1/connections");
      if (epoch !== this.workspaceEpoch) return;
      this.connections = res.connections ?? [];
      this.credentialsConfigured = res.credentials_configured;
    },
    async createConnection(input: {
      provider: string;
      name: string;
      scope: string;
      base_url: string;
      username: string;
      token: string;
    }) {
      await api.post<Connection>("/api/v1/connections", input);
      await this.refreshConnections();
    },
    async updateConnection(
      id: string,
      patch: { name?: string; status?: string; token?: string },
    ) {
      await api.patch<Connection>(`/api/v1/connections/${id}`, patch);
      await this.refreshConnections();
    },
    async testConnection(id: string) {
      await api.post<Connection>(`/api/v1/connections/${id}/test`);
      await this.refreshConnections();
    },
    async deleteConnection(id: string) {
      await api.del(`/api/v1/connections/${id}`);
      await this.refreshConnections();
    },
    async bindProject(
      id: string,
      binding: { git_connection_id: string; remote_repo: string },
    ) {
      const res = await api.put<Project>(
        `/api/v1/projects/${id}/binding`,
        binding,
      );
      this.projects = this.projects.map((p) => (p.id === id ? res : p));
      return res;
    },
    lookupJiraIssue(connection_id: string, key: string) {
      return api.post<IssueDraft>("/api/v1/integrations/jira/lookup", {
        connection_id,
        key,
      });
    },
    async createProject(input: {
      name: string;
      repo_url: string;
      language: string;
    }) {
      const res = await api.post<Project>("/api/v1/projects", input);
      this.projects = [...this.projects.filter((p) => p.id !== res.id), res];
      await this.refreshProjects().catch(() => {});
      return res;
    },
    async createWorkOrder(input: {
      project_id: string;
      title: string;
      objective: string;
      acceptance: string;
      autonomy: string;
      builder_model: string;
      inspector_model: string;
      source_ref?: string;
    }) {
      const res = await api.post<WorkOrder>("/api/v1/work-orders", input);
      this.workOrders = [
        res,
        ...this.workOrders.filter((w) => w.id !== res.id),
      ];
      await this.refreshWorkOrders().catch(() => {});
      return res;
    },
    async loadDetail(id: string) {
      const request = ++this.requestID;
      if (this.selectedID !== id) {
        this.detail = null;
        this.events = [];
        this.quality = [];
        this.findings = [];
      }
      this.selectedID = id;
      this.loading = true;
      this.error = "";
      try {
        const [detail, events, quality, findings] = await Promise.all([
          api.get<WODetail>(`/api/v1/work-orders/${id}`),
          api.get<{ events: WOEvent[] }>(`/api/v1/work-orders/${id}/activity`),
          api.get<{ checks: QualityCheck[] }>(
            `/api/v1/work-orders/${id}/quality`,
          ),
          api.get<{ findings: Finding[] }>(
            `/api/v1/work-orders/${id}/findings`,
          ),
        ]);
        if (request !== this.requestID) return;
        this.detail = {
          ...detail,
          plan: detail.plan ?? [],
          runs: detail.runs ?? [],
        };
        this.quality = quality.checks ?? [];
        this.findings = findings.findings ?? [];
        this.events = events.events ?? [];
      } catch (e) {
        if (request === this.requestID)
          this.error = e instanceof Error ? e.message : String(e);
      } finally {
        if (request === this.requestID) this.loading = false;
      }
    },
    async reloadDetail() {
      if (this.selectedID) await this.loadDetail(this.selectedID);
    },
    async planGenerate(id: string) {
      await api.post(`/api/v1/work-orders/${id}/plan/generate`);
      await this.reloadDetail();
    },
    async planDecision(id: string, decision: "approve" | "revise", notes = "") {
      await api.post(`/api/v1/work-orders/${id}/plan/decisions`, {
        decision,
        notes,
      });
      await this.reloadDetail();
    },
    async answerClarification(id: string, answer: string) {
      await api.post(`/api/v1/work-orders/${id}/clarifications`, { answer });
      await this.reloadDetail();
    },
    async prDecision(
      id: string,
      decision: "approve" | "request_changes" | "decline",
      reason = "",
    ) {
      await api.post(`/api/v1/work-orders/${id}/pr-decision`, {
        decision,
        reason,
      });
      await this.reloadDetail();
    },
    async pause(id: string, reason = "") {
      await api.post(`/api/v1/work-orders/${id}/pause`, { reason });
      await this.reloadDetail();
    },
    async resume(id: string) {
      await api.post(`/api/v1/work-orders/${id}/resume`);
      await this.reloadDetail();
    },
    async decideFinding(
      id: string,
      findingID: string,
      action: "confirm" | "dispute" | "resolve" | "escalate",
      note = "",
    ) {
      await api.post(
        `/api/v1/work-orders/${id}/findings/${findingID}/decision`,
        {
          action,
          note,
        },
      );
      await this.reloadDetail();
    },
    async cancel(id: string) {
      await api.post(`/api/v1/work-orders/${id}/cancel`);
      await this.reloadDetail();
    },
    async saveSettings(s: Settings) {
      await api.put("/api/v1/settings", s);
    },
    async loadSettings(): Promise<Settings> {
      return api.get<Settings>("/api/v1/settings");
    },
  },
});
