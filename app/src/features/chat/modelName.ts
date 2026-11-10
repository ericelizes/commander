/** Display the reported model; never infer one from the provider. */
export function modelName(model?: string | null): string {
  if (!model) return ''
  const raw = model.replace(/^codex:/, '')
  const codex = /^gpt-(\d+(?:\.\d+)?)-(sol|astra|terra|luna)(.*)$/.exec(raw)
  if (codex) return `${codex[2][0].toUpperCase()}${codex[2].slice(1)} ${codex[1]}${codex[3]}`
  const claude = /^claude-([a-z]+)-(\d+)-(\d+)/.exec(raw)
  if (claude) return `${claude[1][0].toUpperCase()}${claude[1].slice(1)} ${claude[2]}.${claude[3]}`
  return raw
}
