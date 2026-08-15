// Booky-authored types for the vendored foliate-js view — just the surface
// Reader.tsx touches.

export interface TOCItem {
  label: string
  href?: string
  subitems?: TOCItem[] | null
}

export interface RelocateDetail {
  fraction?: number
  cfi?: string
  tocItem?: TOCItem | null
  location?: { current: number; next: number; total: number }
  section?: { current: number; total: number }
}

export interface FoliateRenderer extends HTMLElement {
  setStyles?: (css: string) => void
  next: (distance?: number) => Promise<void>
  prev: (distance?: number) => Promise<void>
}

export class View extends HTMLElement {
  book: {
    metadata?: { title?: unknown; author?: unknown }
    toc?: TOCItem[]
    dir?: string
  }
  renderer: FoliateRenderer
  lastLocation?: RelocateDetail
  open(book: File | Blob | string): Promise<void>
  close(): void
  init(opts: { lastLocation?: string | null; showTextStart?: boolean }): Promise<void>
  goTo(target: string | number): Promise<unknown>
  goToFraction(fraction: number): Promise<void>
  goLeft(): Promise<unknown> | void
  goRight(): Promise<unknown> | void
  prev(distance?: number): Promise<void>
  next(distance?: number): Promise<void>
}
