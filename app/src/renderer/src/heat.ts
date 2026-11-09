// How hot the fleet is running, for the flame by the user's name: agents
// working now set the level, no finished steps in the last half hour caps it
// at an ember, and each agent that died in the last ten minutes drops it one.

export type HeatLevel = 0 | 1 | 2 | 3

export interface Heat {
  level: HeatLevel
  working: number
  done30: number | null // steps finished in the last 30 minutes; null until the app has watched that long
  died: number // Operators that died in the last 10 minutes
}

export function heatOf(working: number, done30: number | null, died: number): Heat {
  let level = working === 0 ? 0 : working <= 3 ? 1 : working <= 8 ? 2 : 3
  if (done30 === 0) level = Math.min(level, 1)
  level = Math.max(0, level - died)
  return { level: level as HeatLevel, working, done30, died }
}

export const HEAT_WINDOW_SECS = 30 * 60

/**
 * doneSince is how many steps finished in the window, from samples of the
 * fleet's done total ([unix s, total], oldest first): the total now less the
 * newest sample at least a window old. null when no sample is that old yet.
 */
export function doneSince(samples: [number, number][], nowSec: number, total: number): number | null {
  let base: number | null = null
  for (const [at, n] of samples) if (nowSec - at >= HEAT_WINDOW_SECS) base = n
  return base === null ? null : Math.max(0, total - base)
}

export function heatTitle(h: Heat): string {
  const parts = [`${h.working} agent${h.working === 1 ? '' : 's'} working`]
  parts.push(h.done30 === null ? 'steps done: watching for 30 min first' : `${h.done30} step${h.done30 === 1 ? '' : 's'} done in the last 30 min`)
  if (h.died) parts.push(`${h.died} Operator${h.died === 1 ? '' : 's'} died in the last 10 min`)
  return parts.join(' · ')
}
