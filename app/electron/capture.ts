// A picture of the app window for `cmdr app shot`. webContents.capturePage
// reads the page's own compositor output, so it needs no macOS Screen
// Recording permission (unlike screencapture). Native inspector views laid
// over the page are separate webContents and do not appear in it.

import type { WebContents } from 'electron'

export async function capturePng(wc: Pick<WebContents, 'capturePage'>): Promise<string> {
  const img = await wc.capturePage(undefined, { stayHidden: true })
  if (img.isEmpty()) throw new Error('the window rendered nothing (hidden or minimized?)')
  return img.toPNG().toString('base64')
}
