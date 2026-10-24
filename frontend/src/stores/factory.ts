import { defineStore } from "pinia";
import { subscribe, type LiveEvent } from "../services/events/sse";
import {
  api,
  type Finding,
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
    detail: null as WODetail | null,
    events: [] as WOEvent[],
    quality: [] as QualityCheck[],
    findings: [] as Finding[],
    loading: false,
    error: "" as string,
    selectedID: "",
    requestID: 0,
    stopStream: null as (() => void) | null,
    liveEvents: [] as LiveEvent[],
    connected: false,
  }),
  actions: {
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
      const res = await api.get<{ projects: Project[] }>("/api/v1/projects");
      this.projects = res.projects ?? [];
    },
    async refreshWorkOrders() {
      const res = await api.get<{ work_orders: WorkOrder[] }>(
        "/api/v1/work-orders",
      );
      this.workOrders = res.work_orders ?? [];
    },
    async refreshOverview() {
      this.overview = await api.get<Overview>("/api/v1/overview");
    },
    async refreshModels() {
      const res = await api.get<{ models: ModelInfo[] }>("/api/v1/models");
      this.models = res.models ?? [];
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
