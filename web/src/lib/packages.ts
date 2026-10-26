/**
 * Whether a package is on the never-upgrade list: sysupdate's excluded on
 * the server, held to the same cases (internal/sysupdate/testdata/excluded.json).
 * An entry is a name or a glob, where * stands for any run of characters
 * and ? for one.
 */
export function excluded(name: string, exclude?: string[]) {
  return (exclude ?? []).some((e) => e === name || glob(e).test(name))
}

function glob(pattern: string) {
  const body = pattern.replace(/[.+^${}()|[\]\\*?]/g, (c: string) =>
    c === '*' ? '.*' : c === '?' ? '.' : `\\${c}`,
  )
  return new RegExp(`^${body}$`)
}
