import { readFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'

/** commanderHome is $COMMANDER_HOME, else ~/.commander (same rule as the Go side). */
export function commanderHome(env: NodeJS.ProcessEnv = process.env): string {
  return env.COMMANDER_HOME || join(homedir(), '.commander')
}

function readTrimmed(path: string): string {
  try {
    return readFileSync(path, 'utf8').trim()
  } catch {
    return ''
  }
}

/** apiConfig reads cmdrd's URL and token; COMMANDER_API_URL overrides the URL (tests, offline smoke). */
export function apiConfig(env: NodeJS.ProcessEnv = process.env): { apiUrl: string; token: string } {
  const home = commanderHome(env)
  const apiUrl = env.COMMANDER_API_URL || readTrimmed(join(home, 'api-url')) || 'http://127.0.0.1:8787'
  return { apiUrl: apiUrl.replace(/\/+$/, ''), token: readTrimmed(join(home, 'api-token')) }
}

/**
 * apiRequestOrigin is the Origin to send cmdrd for a request from the app's
 * own page. cmdrd accepts only http(s) loopback Origins; a file:// page sends
 * "null" or (Electron 44 on Linux, for WebSockets) "file://", so either becomes
 * the API's origin. An http(s) Origin (the live dev server) is kept: cmdrd
 * echoes it in CORS and a rewritten one would not match the page.
 */
export function apiRequestOrigin(origin: string | undefined, apiOrigin: string): string {
  return !origin || origin === 'null' || origin.startsWith('file:') ? apiOrigin : origin
}
