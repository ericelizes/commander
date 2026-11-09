// confetti bursts from a point once: the moment a workstream or track is completed.
const COLORS = ['#17864f', '#2463eb', '#b26b00', '#cf3a3a', '#4a55e0', '#6bcb94']

export function confetti(x: number, y: number): void {
  if (window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) return
  const host = document.createElement('div')
  host.className = 'confetti'
  host.setAttribute('aria-hidden', 'true')
  for (let i = 0; i < 36; i++) {
    const bit = document.createElement('span')
    const angle = Math.random() * Math.PI * 2
    const dist = 50 + Math.random() * 90
    bit.style.left = `${x}px`
    bit.style.top = `${y}px`
    bit.style.background = COLORS[i % COLORS.length]
    bit.style.setProperty('--dx', `${Math.cos(angle) * dist}px`)
    bit.style.setProperty('--dy', `${Math.sin(angle) * dist - 40}px`)
    bit.style.setProperty('--rot', `${Math.random() * 720 - 360}deg`)
    host.appendChild(bit)
  }
  document.body.appendChild(host)
  setTimeout(() => host.remove(), 1300)
}
