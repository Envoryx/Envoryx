import { clsx } from "clsx";
import type { LogLevel } from "@/api/types";

export interface ShownLine {
  id: number | string;
  time: string;
  stream: "stdout" | "stderr";
  text: string;
  level?: LogLevel | undefined;
}

/** Line filter of the log views: everything, one stream or a minimum level. */
export type LineFilter = "all" | "stderr" | "warn" | "error";

export function lineMatches(l: ShownLine, filter: LineFilter, q: string): boolean {
  if (filter === "stderr" && l.stream !== "stderr") return false;
  if (filter === "warn" && !l.level) return false;
  if (filter === "error" && l.level !== "error") return false;
  return !q || l.text.toLowerCase().includes(q);
}

export function formatLogTime(iso: string, withDate = false): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "";
  const d = new Date(t);
  const time = d.toLocaleTimeString(undefined, { hour12: false });
  return withDate ? `${d.toLocaleDateString(undefined, { day: "2-digit", month: "2-digit" })} ${time}` : time;
}

/** The dark terminal-style table both log views use. Errors red, warnings amber. */
export function LogLinesTable({ lines, withDate = false }: { lines: ShownLine[]; withDate?: boolean }) {
  return (
    <table className="w-full border-collapse">
      <tbody>
        {lines.map((l) => (
          <tr
            key={l.id}
            className={clsx("align-top hover:bg-white/5", l.level === "error" ? "text-red-300" : l.level === "warn" ? "text-amber-200" : l.stream === "stderr" && "text-zinc-300")}
          >
            <td className={clsx("select-none whitespace-nowrap py-0 pl-3 pr-2 text-zinc-500", withDate ? "w-32" : "w-20")}>{formatLogTime(l.time, withDate)}</td>
            <td className="whitespace-pre-wrap break-all py-0 pr-3">{l.text}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
