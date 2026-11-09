// The right-click menu. Electron draws none of its own, so without this a
// link, a selection or a text field offers nothing on right click.

import { clipboard, Menu, shell, type ContextMenuParams, type MenuItemConstructorOptions, type WebContents } from 'electron'

/** contextTemplate is the menu for one right click, built from what is under the pointer. */
export function contextTemplate(p: Pick<ContextMenuParams, 'linkURL' | 'selectionText' | 'isEditable' | 'editFlags'>): MenuItemConstructorOptions[] {
  const groups: MenuItemConstructorOptions[][] = []
  if (/^(https?|mailto):/.test(p.linkURL)) {
    groups.push([
      { label: 'Open Link in Browser', click: () => void shell.openExternal(p.linkURL) },
      { label: 'Copy Link', click: () => clipboard.writeText(p.linkURL.replace(/^mailto:/, '')) },
    ])
  }
  if (p.isEditable) {
    groups.push([
      { role: 'cut', enabled: p.editFlags.canCut },
      { role: 'copy', enabled: p.editFlags.canCopy },
      { role: 'paste', enabled: p.editFlags.canPaste },
      { role: 'selectAll' },
    ])
  } else if (p.selectionText.trim()) {
    groups.push([{ role: 'copy' }])
  }
  return groups.flatMap((g, i) => (i ? [{ type: 'separator' as const }, ...g] : g))
}

/** attachContextMenu shows contextTemplate on every right click in wc. */
export function attachContextMenu(wc: WebContents): void {
  wc.on('context-menu', (_e, params) => {
    const template = contextTemplate(params)
    if (template.length) Menu.buildFromTemplate(template).popup()
  })
}
