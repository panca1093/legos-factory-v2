import { describe, expect, it, vi, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useFactory } from '../../src/stores/factory'
import { useSession } from '../../src/stores/session'
import { ApiError } from '../../src/types/api'

const fetchMock = vi.fn()

beforeEach(() => {
  setActivePinia(createPinia())
  vi.stubGlobal('fetch', fetchMock)
  fetchMock.mockReset()
})

function jsonBody(status: number, data: unknown) {
  return { ok: status < 400, status, text: () => Promise.resolve(JSON.stringify(data)) }
}

describe('factory store', () => {
  it('lists work orders from the API', async () => {
    fetchMock.mockResolvedValue(jsonBody(200, { work_orders: [{ id: 'wo_1', state: 'READY' }] }))
    const f = useFactory()
    await f.refreshWorkOrders()
    expect(f.workOrders).toHaveLength(1)
    expect(f.workOrders[0].state).toBe('READY')
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/work-orders', expect.objectContaining({ method: 'GET' }))
  })

  it('surfaces API error messages on failed requests', async () => {
    fetchMock.mockResolvedValue(jsonBody(409, { error: { code: 'INVALID_STATE_TRANSITION', message: 'nope' } }))
    const f = useFactory()
    await expect(f.planDecision('wo_1', 'approve')).rejects.toMatchObject({ status: 409, code: 'INVALID_STATE_TRANSITION' })
  })

  it('ApiError carries status and code', () => {
    const err = new ApiError(403, 'FORBIDDEN', 'Admin role required')
    expect(err.status).toBe(403)
    expect(err.code).toBe('FORBIDDEN')
    expect(err.message).toBe('Admin role required')
  })
})

describe('session store', () => {
  it('falls back to dev-login when session is missing', async () => {
    fetchMock
      .mockResolvedValueOnce(jsonBody(401, { error: { code: 'UNAUTHENTICATED', message: 'x' } }))
      .mockResolvedValueOnce(jsonBody(200, { user: { id: 'u', email: 'e', name: 'N', role: 'OWNER' }, mode: 'dev' }))
    const s = useSession()
    await s.bootstrap()
    expect(s.user?.role).toBe('OWNER')
    expect(s.canWrite).toBe(true)
    expect(s.isAdmin).toBe(true)
  })

  it('viewer cannot write', async () => {
    fetchMock.mockResolvedValue(jsonBody(200, { user: { id: 'u', email: 'e', name: 'N', role: 'VIEWER' }, mode: 'dev' }))
    const s = useSession()
    await s.bootstrap()
    expect(s.canWrite).toBe(false)
    expect(s.isAdmin).toBe(false)
  })
})
