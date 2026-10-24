const storageKey = "starlogz.lastProject";

// Storage can be unavailable or blocked; the preference is only a convenience.
export function readLastProject(): string | null {
  try {
    return window.localStorage.getItem(storageKey);
  } catch {
    return null;
  }
}

export function writeLastProject(slug: string) {
  try {
    window.localStorage.setItem(storageKey, slug);
  } catch {}
}
