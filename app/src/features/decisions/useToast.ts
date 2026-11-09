import { useCallback, useEffect, useRef, useState } from 'react'

// A short-lived status message ("Copied"), announced through a role=status region.
export function useToast(ms = 1800) {
  const [toast, setToast] = useState<{ text: string, tone: 'ok' | 'err' } | null>(null)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const show = useCallback((text: string, tone: 'ok' | 'err' = 'ok') => {
    if (timer.current) clearTimeout(timer.current)
    setToast({ text, tone })
    timer.current = setTimeout(() => setToast(null), ms)
  }, [ms])
  useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current)
  }, [])
  return { toast, show }
}
