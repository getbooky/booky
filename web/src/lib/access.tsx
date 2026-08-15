import { createContext, useContext } from "react"
import type { ApiUser } from "@/api"

// Who is signed in and what the UI should offer them.
//
// The server is the actual gate — every rule here has a matching check in the
// Go handlers, and this only decides what to render. Hiding a control the API
// would refuse is the point: a plain user should not be shown an "Edit
// metadata" button that answers 403.
//
// A null user means no account exists yet (the API is open for the setup
// wizard), which the server also treats as admin — so the two agree.

export interface Access {
  user: ApiUser | null
  isAdmin: boolean
  /** Libraries this account may work in. Empty for admins, who reach all. */
  libraryIds: number[]
}

const AccessContext = createContext<Access>({ user: null, isAdmin: true, libraryIds: [] })

export const AccessProvider = AccessContext.Provider

export function accessFor(user: ApiUser | null): Access {
  return {
    user,
    isAdmin: user === null || user.role === "admin",
    libraryIds: user?.libraryIds ?? [],
  }
}

export function useAccess(): Access {
  return useContext(AccessContext)
}

/** Shorthand for the common "should this control render at all" check. */
export function useIsAdmin(): boolean {
  return useContext(AccessContext).isAdmin
}
