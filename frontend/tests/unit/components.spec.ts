import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import BrickBot from '../../src/components/ui/BrickBot.vue'
import StatusBadge from '../../src/components/ui/StatusBadge.vue'

describe('BrickBot', () => {
  it('renders five distinct personas with role-specific accessories', () => {
    const personas = ['foreman', 'architect', 'builder', 'inspector', 'cataloger'] as const
    for (const persona of personas) {
      const w = mount(BrickBot, { props: { persona } })
      expect(w.attributes('aria-label')).toContain(persona)
      expect(w.html()).toContain('<rect')
    }
  })
})

describe('StatusBadge', () => {
  it('maps states to severity styles and readable labels', () => {
    expect(mount(StatusBadge, { props: { state: 'READY' } }).classes()).toContain('mint')
    expect(mount(StatusBadge, { props: { state: 'BLOCKED' } }).classes()).toContain('red')
    expect(mount(StatusBadge, { props: { state: 'WAITING_FOR_HUMAN' } }).classes()).toContain('amber')
    expect(mount(StatusBadge, { props: { state: 'NEEDS_HUMAN_REVIEW' } }).text()).toBe('needs human review')
  })
})
