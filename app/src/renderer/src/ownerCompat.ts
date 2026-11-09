// Read snapshots from a daemon that predates the neutral owner vocabulary.
// Normalize only identity/state fields, never free text or decision evidence.
export function normalizeOwnerSnapshot<T>(value: T): T {
  if (Array.isArray(value)) return value.map(normalizeOwnerSnapshot) as T
  if (!value || typeof value !== 'object') return value
  const out: Record<string, unknown> = {}
  const normalize = (v: unknown) => v === 'user' ? 'user' : v === 'needs-user' ? 'needs-user' : v
  for (const [key, v] of Object.entries(value)) {
    out[key] = ['state', 'owner', 'verifier', 'waiting_kind'].includes(key)
      ? normalize(v)
      : key === 'approvers' && Array.isArray(v) ? v.map(normalize) : normalizeOwnerSnapshot(v)
  }
  return out as T
}
