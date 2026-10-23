const scpLike = /^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[A-Za-z0-9._/~-]+$/;

/**
 * Why the server would refuse a repository URL (project.ValidateGitURL), as the message
 * it sends; undefined when the URL passes or is empty. While the URL still reads as the
 * start of a scheme ("ht", "git@gi") nothing is said yet.
 */
export function gitUrlError(raw: string, typing = false): string | undefined {
  const s = raw.trim();
  if (!s) return undefined;
  if (s.startsWith("-") || /[\s'"`$;|&<>\\]/.test(s)) return "repository URL contains unsupported characters";
  if (scpLike.test(s)) return undefined;
  const scheme = /^([A-Za-z][A-Za-z0-9+.-]*):\/\//.exec(s)?.[1]?.toLowerCase();
  if (!scheme) {
    if (typing && ["https://", "http://", "ssh://", "git@"].some((p) => p.startsWith(s.toLowerCase()) || (p === "git@" && s.startsWith(p)))) return undefined;
    return "repository URL must use https://, ssh:// or git@host:path";
  }
  if (scheme !== "https" && scheme !== "http" && scheme !== "ssh") return "repository URL must use https://, ssh:// or git@host:path";
  let u: URL;
  try {
    u = new URL(s);
  } catch {
    return "invalid repository URL";
  }
  if (!u.host || u.pathname === "" || u.pathname === "/") return typing ? undefined : "repository URL must contain a host and a path";
  if (u.password) return "do not put credentials into the URL; use the access token field";
  return undefined;
}
