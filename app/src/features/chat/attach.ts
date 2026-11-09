// Files for the composer: read to data URLs, images downscaled to what claude
// can use (long edge 1568px, under 5 MB, the API's per-image limit).
import type { Attachment } from './transport'

export const MAX_EDGE = 1568
const IMAGE_LIMIT = 4 * 1024 * 1024
const FILE_LIMIT = 25 * 1024 * 1024
const SENDABLE_IMAGES = ['image/png', 'image/jpeg', 'image/gif', 'image/webp']

export const isImage = (mediaType: string) => SENDABLE_IMAGES.includes(mediaType)

function readDataUrl(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader()
    r.onload = () => resolve(String(r.result))
    r.onerror = () => reject(r.error)
    r.readAsDataURL(blob)
  })
}

// Shrinks a big png/jpeg/webp (gifs keep their frames). Returns the file itself
// when it already fits or the browser cannot draw it.
export async function downscale(file: File): Promise<Blob> {
  if (file.type === 'image/gif' || typeof createImageBitmap !== 'function' || typeof document === 'undefined') return file
  try {
    const bmp = await createImageBitmap(file)
    const scale = Math.min(1, MAX_EDGE / Math.max(bmp.width, bmp.height))
    if (scale === 1 && file.size <= IMAGE_LIMIT) return file
    const canvas = document.createElement('canvas')
    canvas.width = Math.round(bmp.width * scale)
    canvas.height = Math.round(bmp.height * scale)
    canvas.getContext('2d')?.drawImage(bmp, 0, 0, canvas.width, canvas.height)
    const toBlob = (type: string, q?: number) => new Promise<Blob | null>((res) => canvas.toBlob(res, type, q))
    const out = (file.type === 'image/png' ? await toBlob('image/png') : null) ?? file
    if (out !== file && out.size <= IMAGE_LIMIT) return out
    return (await toBlob('image/jpeg', 0.85)) ?? file
  } catch {
    return file
  }
}

// readAttachment: a file as the composer holds it, or an error line to show.
export async function readAttachment(file: File): Promise<Attachment | string> {
  const name = file.name || (isImage(file.type) ? 'pasted image' : 'file')
  if (file.size > FILE_LIMIT) return `${name} is over 25 MB`
  if (!isImage(file.type)) return { name, mediaType: file.type || 'application/octet-stream', dataUrl: await readDataUrl(file) }
  const blob = await downscale(file)
  if (blob.size > 5 * 1024 * 1024) return `${name} is over 5 MB as an image`
  const name2 = blob.type && blob.type !== file.type ? name.replace(/\.\w+$/, '') + '.jpg' : name
  return { name: name2, mediaType: blob.type || file.type, dataUrl: await readDataUrl(blob) }
}
