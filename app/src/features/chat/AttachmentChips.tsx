import type { ItemAttachment } from './events'
import { Icon } from './icons'

// Files on a sent message: a thumbnail for an image this session sent, else the name.
export function AttachmentChips({ list }: { list?: ItemAttachment[] }) {
  if (!list?.length) return null
  return (
    <div className="cc-sent-files">
      {list.map((a, i) =>
        a.dataUrl && a.mediaType.startsWith('image/') ? (
          <img key={i} className="cc-sent-thumb" src={a.dataUrl} alt={a.name} title={a.name} />
        ) : (
          <span key={i} className="cc-sent-file" title={a.name}>
            <Icon name={a.mediaType.startsWith('image/') ? 'image' : 'file'} size={12} />
            <span>{a.name}</span>
          </span>
        ),
      )}
    </div>
  )
}
