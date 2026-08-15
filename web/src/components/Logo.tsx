// The Booky mark: a brass tile with an open book. Used beside the wordmark
// and (as SVG) for the favicon.
export function Logo({ size = 30 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 32 32" aria-hidden="true">
      <rect x="1" y="1" width="30" height="30" rx="9" fill="hsl(var(--brass))" />
      <g stroke="hsl(var(--brass-ink))" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" fill="none">
        <path d="M16 10.5c-1.8-1.6-4.4-2.1-7-1.4v12.4c2.6-.7 5.2-.2 7 1.4 1.8-1.6 4.4-2.1 7-1.4V9.1c-2.6-.7-5.2-.2-7 1.4z" />
        <path d="M16 10.5v12.4" />
      </g>
    </svg>
  )
}
