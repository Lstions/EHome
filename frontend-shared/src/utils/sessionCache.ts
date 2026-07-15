const cacheClearers = new Set<() => void>()
let sessionGeneration = 0

export function registerSessionCacheClearer(clearer: () => void): () => void {
  cacheClearers.add(clearer)
  return () => cacheClearers.delete(clearer)
}

export function clearSessionCaches(): void {
  sessionGeneration++
  for (const clear of cacheClearers) clear()
}

export function getSessionGeneration(): number {
  return sessionGeneration
}

export function assertSessionGeneration(generation: number): void {
  if (generation !== sessionGeneration) throw new Error('会话已变更')
}