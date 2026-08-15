import { useState } from "react"
import { authorPhotoUrl } from "@/api"
import type { ApiAuthor } from "@/api"
import { cn } from "@/lib/utils"

const AVATAR_GRADS: [string, string][] = [
  ["#7b4bb8", "#b84b7e"], ["#2c5a6e", "#1c3f5e"], ["#a3502a", "#69201c"],
  ["#245a63", "#274a33"], ["#6e2c50", "#5a2440"], ["#3a5a2a", "#152309"],
]

function initials(name: string) {
  const parts = name.split(/\s+/).filter(Boolean)
  return ((parts[0]?.[0] ?? "") + (parts[parts.length - 1]?.[0] ?? "")).toUpperCase()
}

// AuthorAvatar shows the author's Hardcover portrait when one is synced,
// falling back to an initials medallion (deterministic gradient per author).
export function AuthorAvatar({ author, className, textClass }: {
  author: ApiAuthor
  className?: string
  textClass?: string
}) {
  const [imgOk, setImgOk] = useState(true)
  const grad = AVATAR_GRADS[Number(author.id) % AVATAR_GRADS.length]
  const showPhoto = author.hasPhoto && imgOk
  return (
    <div
      className={cn("font-book relative flex shrink-0 items-center justify-center overflow-hidden rounded-full font-bold text-white", className)}
      style={{ background: `linear-gradient(140deg, ${grad[0]}, ${grad[1]})` }}
    >
      <span className={textClass}>{initials(author.name)}</span>
      {showPhoto && (
        <img src={authorPhotoUrl(author.id)} alt={author.name} loading="lazy"
          onError={() => setImgOk(false)}
          className="absolute inset-0 h-full w-full object-cover" />
      )}
    </div>
  )
}
