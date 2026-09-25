/**
 * Reading and writing .env files the way Laravel, Symfony and docker compose do:
 * `KEY=value`, `export KEY=value`, comments, single quotes (literal), double quotes
 * (escapes, may span lines) and `${OTHER}` references to keys defined above.
 */

export interface DotenvEntry {
  key: string;
  value: string;
  /** The line the entry starts on (1-based). */
  line: number;
}

export interface DotenvProblem {
  line: number;
  text: string;
}

export interface DotenvResult {
  entries: DotenvEntry[];
  problems: DotenvProblem[];
}

const keyRe = /^[A-Za-z_][A-Za-z0-9_.]*$/;

/** Replaces ${KEY} and $KEY with values defined earlier in the file (or leaves them). */
function interpolate(value: string, known: Map<string, string>): string {
  return value.replace(/\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)/g, (m, braced: string | undefined, bare: string | undefined) => {
    const name = braced ?? bare ?? "";
    return known.has(name) ? known.get(name)! : m;
  });
}

export function parseDotenv(text: string): DotenvResult {
  const lines = text.replace(/^\uFEFF/, "").split(/\r?\n/);
  const entries: DotenvEntry[] = [];
  const problems: DotenvProblem[] = [];
  const known = new Map<string, string>();
  for (let i = 0; i < lines.length; i++) {
    const lineNo = i + 1;
    let line = lines[i]!.trim();
    if (!line || line.startsWith("#")) continue;
    line = line.replace(/^export\s+/, "");
    const eq = line.indexOf("=");
    if (eq <= 0) {
      problems.push({ line: lineNo, text: lines[i]!.trim() });
      continue;
    }
    const key = line.slice(0, eq).trim();
    let rest = line.slice(eq + 1).trimStart();
    if (!keyRe.test(key)) {
      problems.push({ line: lineNo, text: lines[i]!.trim() });
      continue;
    }
    let value: string;
    if (rest.startsWith("'")) {
      const end = rest.indexOf("'", 1);
      value = end < 0 ? rest.slice(1) : rest.slice(1, end);
    } else if (rest.startsWith('"')) {
      // A double-quoted value may go on over the next lines until the closing quote.
      let body = rest.slice(1);
      let closed = findClosingQuote(body);
      while (closed < 0 && i + 1 < lines.length) {
        i++;
        body += "\n" + lines[i]!;
        closed = findClosingQuote(body);
      }
      const raw = closed < 0 ? body : body.slice(0, closed);
      value = interpolate(
        raw.replace(/\\([nrt"\\$])/g, (_, c: string) => ({ n: "\n", r: "\r", t: "\t" } as Record<string, string>)[c] ?? c),
        known,
      );
    } else {
      // Unquoted: a comment starts at " #".
      const hash = rest.search(/\s#/);
      if (hash >= 0) rest = rest.slice(0, hash);
      value = interpolate(rest.trim(), known);
    }
    known.set(key, value);
    entries.push({ key, value, line: lineNo });
  }
  // A key given twice counts with its last value, as every .env reader does.
  const last = new Map<string, DotenvEntry>();
  for (const e of entries) last.set(e.key, e);
  return { entries: [...last.values()].sort((a, b) => a.line - b.line), problems };
}

function findClosingQuote(s: string): number {
  for (let i = 0; i < s.length; i++) {
    if (s[i] === "\\") {
      i++;
      continue;
    }
    if (s[i] === '"') return i;
  }
  return -1;
}

/** Writes variables as a .env file; values that need it are double-quoted. */
export function serializeDotenv(vars: { key: string; value: string }[], header?: string): string {
  const out: string[] = [];
  if (header) out.push(...header.split("\n").map((l) => `# ${l}`), "");
  for (const { key, value } of vars) {
    if (!key) continue;
    out.push(`${key}=${quote(value)}`);
  }
  return out.join("\n") + "\n";
}

function quote(value: string): string {
  if (value === "" || /^[A-Za-z0-9_./:@%+,=-]+$/.test(value)) return value;
  return `"${value.replace(/\\/g, "\\\\").replace(/"/g, '\\"').replace(/\$/g, "\\$").replace(/\n/g, "\\n")}"`;
}

/** Keys whose values are credentials by the usual naming conventions. */
const secretKeyRe = /(PASSWORD|PASSWD|PASS$|SECRET|TOKEN|PRIVATE|CREDENTIAL|API_?KEY|APP_KEY|AUTH_KEY|_KEY$|_SALT$|SALT$|SIGNATURE|WEBHOOK)/i;

/** Whether a variable looks like a secret: by its name, or a URL/DSN that carries a password. */
export function looksSecret(key: string, value: string): boolean {
  return secretKeyRe.test(key) || /^[a-z][a-z0-9+.-]*:\/\/[^/\s:@]+:[^/\s@]+@/i.test(value);
}
