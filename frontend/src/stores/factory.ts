import { defineStore } from 'pinia'
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
} from '../types/api'

export interface WODetail {
  work_order: WorkOrder
  plan: Task[]
  runs: Run[]
}

export const useFactory = defineStore('factory', {
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
    error: '' as string,
  }),
  actions: {
    async refreshProjects() {
      const res = await api.get<{ projects: Project[] }>('/api/v1/projects')
      this.projects = res.projects
    },
    async refreshWorkOrders() {
      const res = await api.get<{ work_orders: WorkOrder[] }>('/api/v1/work-orders')
      this.workOrders = res.work_orders
    },
    async refreshOverview() {
      this.overview = await api.get<Overview>('/api/v1/overview')
    },
    async refreshModels() {
      const res = await api.get<{ models: ModelInfo[] }>('/api/v1/models')
      this.models = res.models
    },
    async createProject(input: { name: string; repo_url: string; language: string }) {
      const res = await api.post<Project>('/api/v1/projects', input)
      await this.refreshProjects()
      return res
    },
    async createWorkOrder(input: {
      project_id: string
      title: string
      objective: string
      acceptance: string
      autonomy: string
      builder_model: string
      inspector_model: string
    }) {
      const res = await api.post<WorkOrder>('/api/v1/work-orders', input)
      await this.refreshWorkOrders()
      return res
    },
    async loadDetail(id: string) {
      this.loading = true
      this.error = ''
      try {
        this.detail = await api.get<WODetail>(`/api/v1/work-orders/${id}`)
        const [events, quality, findings] = await Promise.all([
          api.get<{ events: WOEvent[] }>(`/api/v1/work-orders/${id}/activity`),
          api.get<{ checks: QualityCheck[] }>(`/api/v1/work-orders/${id}/quality`),
          api.get<{ findings: Finding[] }>(`/api/v1/work-orders/${id}/findings`),
        ])
        this.quality = quality.checks
        this.findings = findings.findings
        this.events = events.events
      } catch (e) {
        this.error = e instanceof Error ? e.message : String(e)
      } finally {
        this.loading = false
      }
    },
    async reloadDetail() {
      if (this.detail) await this.loadDetail(this.detail.work_order.id)
    },
    async planGenerate(id: string) {
      await api.post(`/api/v1/work-orders/${id}/plan/generate`)
      await this.reloadDetail()
    },
    async planDecision(id: string, decision: 'approve' | 'revise', notes = '') {
      await api.post(`/api/v1/work-orders/${id}/plan/decisions`, { decision, notes })
      await this.reloadDetail()
    },
    async answerClarification(id: string, answer: string) {
      await api.post(`/api/v1/work-orders/${id}/clarifications`, { answer })
      await this.reloadDetail()
    },
    async prDecision(id: string, decision: 'approve' | 'request_changes' | 'decline', reason = '') {
      await api.post(`/api/v1/work-orders/${id}/pr-decision`, { decision, reason })
      await this.reloadDetail()
    },
    async cancel(id: string) {
      await api.post(`/api/v1/work-orders/${id}/cancel`)
      await this.reloadDetail()
    },
    async saveSettings(s: Settings) {
      await api.put('/api/v1/settings', s)
    },
    async loadSettings(): Promise<Settings> {
      return api.get<Settings>('/api/v1/settings')
    },
  },
})
