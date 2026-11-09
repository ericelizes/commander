// What the user's app shows as he sends a message rides along with it, so the
// agent knows without asking. The block goes to the agent only: the transcript
// strips it, so the user sees what he typed.

const OPEN = '<user-screen>'
const CLOSE = '</user-screen>'
const BLOCK = /^<user-screen>[\s\S]*?<\/user-screen>\s*/

export function withScreen(screen: string, text: string): string {
  return screen ? `${OPEN}\n${screen}\n${CLOSE}\n\n${text}` : text
}

export function stripScreen(text: string): string {
  return text.replace(BLOCK, '')
}
