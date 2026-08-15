// Typed client for the Booky API. Same-origin in production (the Go binary
// serves the app); the Vite dev server proxies /api to localhost:8787.

export interface ApiBook {
  id: number
  authorId: number
  author: string
  seriesId?: number
  seriesName?: string
  seriesNum?: number
  title: string
  description?: string
  language?: string
  publisher?: string
  releaseDate?: string
  goodreadsId?: string
  hardcoverId?: string
  isbn13?: string
  libraryId?: number
  monitored: boolean
  filePath?: string
  fileFormat?: string
  fileSize?: number
  addedAt?: string
  genres?: string[]
  ratingsCount?: number
  fieldLocks?: Record<string, boolean>
}

export interface ApiAuthor {
  id: number
  name: string
  sortName: string
  monitored: boolean
  bookCount: number
  onShelf: number
  bio?: string
  hasPhoto?: boolean
}

export interface ApiSeries {
  id: number
  authorId: number
  author: string
  name: string
  monitored: boolean
  total: number
  onShelf: number
  coverBookIds?: number[]
}

export interface ApiLibrary {
  id: number
  name: string
  rootPath: string
  qualityProfileId: number
  opdsUsername: string
  opdsConfigured?: boolean
  bookCount: number
  onShelf: number
  reviewCount: number
}

export interface SearchResult {
  provider: string
  title: string
  subtitle?: string
  authors?: string[]
  description?: string
  seriesName?: string
  seriesIndex?: number
  isbn13?: string
  goodreadsId?: string
  hardcoverId?: string
  coverUrl?: string
  releaseDate?: string
  inLibrary?: boolean
  monitored?: boolean
}

export interface ScanResult {
  scanned: number
  matched: number
  review: number
  skipped: number
}

export interface ReviewFile {
  id: number
  path: string
  guessTitle: string
  guessAuthor: string
  confidence: number
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    headers: init?.body ? { "Content-Type": "application/json" } : undefined,
    ...init,
  })
  if (!res.ok) {
    let message = `${res.status}`
    try {
      const body = await res.json()
      if (body.error) message = body.error
    } catch { /* non-json error body */ }
    throw new Error(message)
  }
  return res.json() as Promise<T>
}

export const api = {
  status: () => request<{ app: string; version: string; status: string }>("/api/v1/system/status"),
  health: () => request<{ ok: boolean; checks: { name: string; status: "ok" | "error" | "pending"; detail: string }[] }>("/api/v1/system/health"),
  logs: () => request<{ lines: string[] }>("/api/v1/system/logs"),

  search: (q: string) =>
    request<{ results: SearchResult[] | null; knownAuthors: Record<string, boolean> | null; authorImages: Record<string, string> | null }>(`/api/v1/search?q=${encodeURIComponent(q)}`),
  enrich: (meta: SearchResult) =>
    request<SearchResult>("/api/v1/search/enrich", { method: "POST", body: JSON.stringify({ meta }) }),

  books: (opts: { authorId?: number; libraryId?: number; seriesId?: number } = {}) => {
    const params = new URLSearchParams()
    if (opts.authorId) params.set("authorId", String(opts.authorId))
    if (opts.libraryId) params.set("libraryId", String(opts.libraryId))
    if (opts.seriesId) params.set("seriesId", String(opts.seriesId))
    const qs = params.toString()
    return request<{ books: ApiBook[] | null }>(`/api/v1/books${qs ? "?" + qs : ""}`)
  },
  book: (id: number) => request<ApiBook>(`/api/v1/books/${id}`),
  setFieldLock: (id: number, field: string, locked: boolean) =>
    request<{ fieldLocks?: Record<string, boolean> }>(`/api/v1/books/${id}/lock`, {
      method: "PUT", body: JSON.stringify({ field, locked }),
    }),
  addBook: (meta: SearchResult, libraryId: number, monitored = true) =>
    request<ApiBook>("/api/v1/books", {
      method: "POST",
      body: JSON.stringify({ meta, libraryId, monitored }),
    }),
  // these three can each land a different image at the same cover URL, so
  // they bump the cover generation on the way out
  refreshBook: (id: number) => bustAfter(request<ApiBook>(`/api/v1/books/${id}/refresh`, { method: "POST" })),
  rematchBook: (id: number) => bustAfter(request<ApiBook>(`/api/v1/books/${id}/rematch`, { method: "POST" })),
  regenCover: (id: number) => bustAfter(request(`/api/v1/books/${id}/cover`, { method: "POST" })),
  // custom covers replace the cached image and auto-lock the cover field
  setCoverUrl: (id: number, url: string) =>
    bustAfter(request<{ status: string; locked: boolean }>(`/api/v1/books/${id}/cover/custom`, {
      method: "PUT", body: JSON.stringify({ url }),
    })),
  uploadCover: async (id: number, file: File) => {
    const form = new FormData()
    form.append("file", file)
    const res = await fetch(`/api/v1/books/${id}/cover/custom`, { method: "PUT", body: form })
    if (!res.ok) {
      let message = `${res.status}`
      try {
        const body = await res.json()
        if (body.error) message = body.error
      } catch { /* non-json error body */ }
      throw new Error(message)
    }
    bustCovers()
    return res.json() as Promise<{ status: string; locked: boolean }>
  },
  manualImport: (id: number, libraryId: number, path: string, queueId?: number) =>
    request<{ path: string; book: ApiBook }>(`/api/v1/books/${id}/import`, {
      method: "POST", body: JSON.stringify({ libraryId, path, queueId }),
    }),
  // browser-side upload variant: the file comes from the device the user is
  // on, streams to the server's downloads dir, and delivers from there
  manualImportUpload: async (id: number, libraryId: number, file: File, queueId?: number) => {
    const form = new FormData()
    form.append("libraryId", String(libraryId))
    if (queueId) form.append("queueId", String(queueId))
    form.append("file", file)
    const res = await fetch(`/api/v1/books/${id}/import`, { method: "POST", body: form })
    if (!res.ok) {
      let message = `${res.status}`
      try {
        const body = await res.json()
        if (body.error) message = body.error
      } catch { /* non-json error body */ }
      throw new Error(message)
    }
    return res.json() as Promise<{ path: string; book: ApiBook }>
  },
  moveBook: (id: number, fromLibraryId: number, toLibraryId: number) =>
    request(`/api/v1/books/${id}/library`, {
      method: "PUT",
      body: JSON.stringify({ fromLibraryId, toLibraryId }),
    }),
  editBook: (id: number, fields: Record<string, string>, lock = true) =>
    request<ApiBook>(`/api/v1/books/${id}`, {
      method: "PATCH",
      body: JSON.stringify({ fields, lock }),
    }),
  removeBook: (libraryId: number, bookId: number, mode: "library" | "file" | "block") =>
    request(`/api/v1/libraries/${libraryId}/books/${bookId}?mode=${mode}`, { method: "DELETE" }),
  setBookMonitored: (libraryId: number, bookId: number, monitored: boolean) =>
    request(`/api/v1/libraries/${libraryId}/books/${bookId}/monitored`, {
      method: "PUT",
      body: JSON.stringify({ monitored }),
    }),

  // Shelving a series names its library — there is no inferred destination.
  addSeriesToLibrary: (seriesId: number, libraryId: number) =>
    request<{ added: number; queued: number }>(`/api/v1/series/${seriesId}/library`, {
      method: "POST", body: JSON.stringify({ libraryId }),
    }),

  authors: () => request<{ authors: ApiAuthor[] | null }>("/api/v1/authors"),
  addAuthor: (name: string) =>
    request<{ id: number; name: string }>("/api/v1/authors", {
      method: "POST", body: JSON.stringify({ name }),
    }),
  expandAuthor: (id: number) =>
    request<{ added: number }>(`/api/v1/authors/${id}/expand`, { method: "POST", body: "{}" }),
  searchAuthor: (id: number) =>
    request<{ queued: number }>(`/api/v1/authors/${id}/search`, { method: "POST", body: "{}" }),
  deleteAuthor: (id: number, mode: "catalog" | "files" = "catalog") =>
    request<{ status: string; deletedFiles: number }>(`/api/v1/authors/${id}?mode=${mode}`, { method: "DELETE" }),
  // mode "keep" leaves every file on disk; "files" deletes them too. Either
  // way the books drop out of the library.
  deleteLibrary: (id: number, mode: "keep" | "files" = "keep") =>
    request<{ status: string; deletedFiles: number }>(`/api/v1/libraries/${id}?mode=${mode}`, { method: "DELETE" }),
  setLibraryProfile: (libraryId: number, profileId: number) =>
    request(`/api/v1/libraries/${libraryId}/profile`, {
      method: "PUT", body: JSON.stringify({ profileId }),
    }),

  series: () => request<{ series: ApiSeries[] | null }>("/api/v1/series"),

  libraries: () => request<{ libraries: ApiLibrary[] | null }>("/api/v1/libraries"),
  createLibrary: (name: string, rootPath: string) =>
    request<{ id: number }>("/api/v1/libraries", {
      method: "POST",
      body: JSON.stringify({ name, rootPath }),
    }),
  scanLibrary: (id: number) => request<ScanResult>(`/api/v1/libraries/${id}/scan`, { method: "POST" }),
  // search every monitored book that needs something (missing + upgrades); 0 = all libraries
  searchLibrary: (id: number) =>
    request<{ queued: number }>(`/api/v1/libraries/${id}/search`, { method: "POST", body: "{}" }),
  refreshLibrary: (id: number) =>
    request<{ status: string }>(`/api/v1/libraries/${id}/refresh`, { method: "POST" }),
  reviewQueue: (libraryId: number) =>
    request<{ files: ReviewFile[] | null }>(`/api/v1/libraries/${libraryId}/review`),
  reviewIgnore: (fileId: number) =>
    request(`/api/v1/import/review/${fileId}/ignore`, { method: "POST" }),
  reviewMatch: (fileId: number, meta: SearchResult) =>
    request<ApiBook>(`/api/v1/import/review/${fileId}/match`, {
      method: "POST",
      body: JSON.stringify({ meta }),
    }),

  readingProgress: (id: number) =>
    request<{ locator: string; percent: number; updatedAt?: string }>(`/api/v1/books/${id}/progress`),
  saveReadingProgress: (id: number, locator: string, percent: number) =>
    request<{ status: string }>(`/api/v1/books/${id}/progress`, {
      method: "PUT", body: JSON.stringify({ locator, percent }),
    }),

  getSetting: (key: string) => request<{ key: string; value: string }>(`/api/v1/settings/${key}`),
  putSetting: (key: string, value: string) =>
    request(`/api/v1/settings/${key}`, { method: "PUT", body: JSON.stringify({ value }) }),
}

export const coverUrl = (bookId: number) => `/api/v1/covers/${bookId}`

// A cover keeps one URL while its BYTES change — a custom upload, a re-match,
// a regenerate, a refresh that adopted the provider's new art. The server
// asks browsers to revalidate, which covers a reload, but Chromium serves an
// already-decoded <img> from memory for the life of the page without asking
// anyone, so a single-page session would go on showing the old picture.
// Every cover mutation below bumps this generation and the cover components
// fold it into their src, which is a URL the memory cache has never seen.
let coverGeneration = 0
const coverListeners = new Set<() => void>()
export const coverGen = () => coverGeneration
export function subscribeCovers(fn: () => void) {
  coverListeners.add(fn)
  return () => void coverListeners.delete(fn)
}
export function bustCovers() {
  coverGeneration = Date.now()
  for (const fn of coverListeners) fn()
}

// bump once the call lands, passing the response through untouched
function bustAfter<T>(p: Promise<T>): Promise<T> {
  return p.then(r => { bustCovers(); return r })
}
export const bookFileUrl = (bookId: number) => `/api/v1/books/${bookId}/file`

// formats the in-app reader can render (foliate-js); PDFs open elsewhere
const READABLE = new Set(["epub", "kepub", "mobi", "azw3", "azw", "fb2", "cbz"])
export const canRead = (b: Pick<ApiBook, "filePath" | "fileFormat">) => {
  if (!b.filePath) return false
  const fmt = (b.fileFormat || b.filePath.split(".").pop() || "").toLowerCase()
  return READABLE.has(fmt)
}
export const authorPhotoUrl = (authorId: number) => `/api/v1/authors/${authorId}/photo`

/* ---------- acquisition ---------- */

export interface ApiRelease {
  title: string
  source: string
  protocol: string
  format?: string
  sizeBytes?: number
  downloadUrl: string
  infoUrl?: string
  indexer?: string
  score?: number
}

export interface ApiSourceStat {
  name: string
  configured: boolean
  count: number
  error?: string
}

export interface ApiQueueItem {
  id: number
  bookId: number
  libraryId: number
  bookTitle: string
  releaseTitle: string
  source: string
  protocol: string
  status: string
  externalId?: string
  detail?: string
  createdAt: string
  updatedAt: string
}

export interface ApiHistoryItem {
  id: number
  bookId?: number
  bookTitle?: string
  libraryId?: number
  kind: string
  detail?: string
  createdAt: string
}

export interface ApiProfile {
  id: number
  name: string
  formats: string[]
  cutoffFormat: string
  /** accepted release languages, newline-separated; "" = filter off */
  languages: string
  preferredTerms: string
  avoidedTerms: string
}

export const acquisition = {
  releases: (bookId: number, libraryId: number) =>
    request<{ releases: ApiRelease[] | null; sources: ApiSourceStat[] | null }>(`/api/v1/books/${bookId}/releases?libraryId=${libraryId}`),
  grab: (bookId: number, libraryId: number, release: ApiRelease) =>
    request<{ queueId: number }>(`/api/v1/books/${bookId}/grab`, {
      method: "POST",
      body: JSON.stringify({ libraryId, release }),
    }),
  autoGrab: (bookId: number, libraryId: number) =>
    request<{ grabbed: boolean }>(`/api/v1/books/${bookId}/autograb?libraryId=${libraryId}`, { method: "POST", body: "{}" }),
  queue: () => request<{ queue: ApiQueueItem[] | null }>("/api/v1/queue"),
  retryImport: (queueId: number) => request(`/api/v1/queue/${queueId}/retry`, { method: "POST" }),
  wanted: () => request<{ books: ApiBook[] | null; cutoffUnmet: ApiBook[] | null }>("/api/v1/wanted"),
  history: () => request<{ history: ApiHistoryItem[] | null }>("/api/v1/history"),
  profiles: () => request<{ profiles: ApiProfile[] | null }>("/api/v1/profiles"),
  updateProfile: (id: number, p: Partial<ApiProfile>) =>
    request(`/api/v1/profiles/${id}`, { method: "PUT", body: JSON.stringify(p) }),
  testProwlarr: (url: string, apiKey: string) =>
    request<{ version: string; indexers: number }>("/api/v1/system/test/prowlarr", {
      method: "POST", body: JSON.stringify({ url, apiKey }),
    }),
  testSab: (url: string, apiKey: string, category: string) =>
    request<{ version: string }>("/api/v1/system/test/sab", {
      method: "POST", body: JSON.stringify({ url, apiKey, category }),
    }),
  testZlib: () =>
    request<{ downloadsLeft: number; downloadsLimit: number }>("/api/v1/system/test/zlib", {
      method: "POST", body: JSON.stringify({}),
    }),
  testHardcover: (token = "") =>
    request<{ status: string }>("/api/v1/system/test/hardcover", {
      method: "POST", body: JSON.stringify({ token }),
    }),
  testGoodreadsList: (sourceRef: string, shelf = "") =>
    request<{ entries: number; sourceRef: string }>("/api/v1/system/test/grlist", {
      method: "POST", body: JSON.stringify({ sourceRef, shelf }),
    }),
  discoverGoodreads: (sourceRef: string) =>
    request<{ userId: string; shelves: { name: string; count: number }[] }>("/api/v1/system/discover/goodreads", {
      method: "POST", body: JSON.stringify({ sourceRef }),
    }),
  // no user → the token owner's lists; a username/@handle/URL → that user's public lists
  discoverHardcover: (user?: string) =>
    request<{ lists: { id: string; name: string; count: number }[] }>(
      "/api/v1/system/discover/hardcover" + (user ? `?user=${encodeURIComponent(user)}` : "")),
}

/* ---------- watchers ---------- */

export interface ApiWatchedList {
  id: number
  name: string
  kind: "goodreads_rss" | "hardcover"
  sourceRef: string
  libraryId: number
  libraryName?: string
  monitorScope: "book" | "series" | "author"
  onRemove: "nothing" | "unmonitor" | "delete"
  searchOnAdd: boolean
  enabled: boolean
  qualityProfileId?: number
  lastChecked?: string
  lastError?: string
  itemCount: number
}

// Create/update payload: sourceRef takes whatever the user pasted (profile
// URL or ID for Goodreads — canonicalized server-side with shelf — or a
// Hardcover list id).
export interface ListPayload {
  name: string
  kind: "goodreads_rss" | "hardcover"
  sourceRef: string
  shelf?: string
  libraryId: number
  monitorScope: string
  onRemove: string
  searchOnAdd: boolean
  enabled: boolean
  qualityProfileId?: number
}

export const watchers = {
  lists: () => request<{ lists: ApiWatchedList[] | null }>("/api/v1/lists"),
  createList: (p: ListPayload) =>
    request<ApiWatchedList>("/api/v1/lists", { method: "POST", body: JSON.stringify(p) }),
  updateList: (id: number, p: Partial<ListPayload>) =>
    request<ApiWatchedList>(`/api/v1/lists/${id}`, { method: "PUT", body: JSON.stringify(p) }),
  deleteList: (id: number) => request(`/api/v1/lists/${id}`, { method: "DELETE" }),
  pollList: (id: number) =>
    request<{ added: number }>(`/api/v1/lists/${id}/poll`, { method: "POST" }),
  calendar: () => request<{ books: ApiBook[] | null }>("/api/v1/calendar"),
}

/* ---------- delivery ---------- */

export interface ApiUser {
  id: number
  username: string
  role: "admin" | "user"
  createdAt: string
  /** Libraries a 'user' may reach. Always empty for admins, who reach all. */
  libraryIds: number[] | null
}

export interface ApiDevice {
  id: number
  name: string
  libraryIds: number[]
  autoLibraryIds: number[]
  createdAt: string
  lastSync?: string
  /** The account that paired it; users only ever see their own devices. */
  ownerId: number
  /** Only sent to admins, who see everyone's devices and need the label. */
  ownerName?: string
}

export interface ApiBackup {
  name: string
  sizeBytes: number
  createdAt: string
}

export const delivery = {
  me: () => request<{ authRequired: boolean; user?: ApiUser }>("/api/v1/auth/me"),
  login: (username: string, password: string) =>
    request<ApiUser>("/api/v1/auth/login", { method: "POST", body: JSON.stringify({ username, password }) }),
  logout: () => request("/api/v1/auth/logout", { method: "POST" }),

  users: () => request<{ users: ApiUser[] | null }>("/api/v1/users"),
  createUser: (username: string, password: string, role: "admin" | "user", libraryIds: number[] = []) =>
    request<{ id: number }>("/api/v1/users", {
      method: "POST", body: JSON.stringify({ username, password, role, libraryIds }),
    }),
  setUserLibraries: (id: number, libraryIds: number[]) =>
    request<{ libraryIds: number[] }>(`/api/v1/users/${id}/libraries`, {
      method: "PUT", body: JSON.stringify({ libraryIds }),
    }),
  deleteUser: (id: number) => request(`/api/v1/users/${id}`, { method: "DELETE" }),

  setOPDS: (libraryId: number, username: string, password: string) =>
    request<{ feedUrl: string }>(`/api/v1/libraries/${libraryId}/opds`, {
      method: "PUT", body: JSON.stringify({ username, password }),
    }),

  devices: () => request<{ devices: ApiDevice[] | null }>("/api/v1/devices"),
  createDevice: (name: string, libraryIds: number[], autoLibraryIds: number[]) =>
    request<ApiDevice>("/api/v1/devices", {
      method: "POST", body: JSON.stringify({ name, libraryIds, autoLibraryIds }),
    }),
  revokeDevice: (id: number) => request(`/api/v1/devices/${id}`, { method: "DELETE" }),
  pluginZipUrl: (id: number) => `/api/v1/devices/${id}/plugin.zip`,

  backups: () => request<{ backups: ApiBackup[] | null }>("/api/v1/backups"),
  createBackup: () => request<{ name: string }>("/api/v1/backups", { method: "POST" }),
  restoreBackup: (name: string) =>
    request(`/api/v1/backups/${encodeURIComponent(name)}/restore`, { method: "POST" }),

  restart: () => request("/api/v1/system/restart", { method: "POST" }),
}
