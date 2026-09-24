import type { TFunction } from "i18next";

/** The schedule shapes the form builds; anything else is edited as a raw cron expression. */
export type ScheduleKind = "minute" | "every" | "hourly" | "daily" | "weekly" | "monthly" | "custom";

export interface ScheduleForm {
  kind: ScheduleKind;
  /** Interval in minutes for "every". */
  every: number;
  /** Minute of the hour for hourly/daily/weekly/monthly. */
  minute: number;
  hour: number;
  /** 0 = Sunday … 6 = Saturday. */
  weekday: number;
  /** Day of the month, 1-28 so every month has it. */
  day: number;
  custom: string;
}

export const everyOptions = [2, 5, 10, 15, 20, 30];

export const defaultSchedule: ScheduleForm = { kind: "daily", every: 5, minute: 0, hour: 3, weekday: 1, day: 1, custom: "" };

export function toCron(f: ScheduleForm): string {
  switch (f.kind) {
    case "minute":
      return "* * * * *";
    case "every":
      return `*/${f.every} * * * *`;
    case "hourly":
      return `${f.minute} * * * *`;
    case "daily":
      return `${f.minute} ${f.hour} * * *`;
    case "weekly":
      return `${f.minute} ${f.hour} * * ${f.weekday}`;
    case "monthly":
      return `${f.minute} ${f.hour} ${f.day} * *`;
    case "custom":
      return f.custom.trim();
  }
}

const num = (s: string, min: number, max: number): number | null => {
  if (!/^\d{1,2}$/.test(s)) return null;
  const n = Number(s);
  return n >= min && n <= max ? n : null;
};

/** Reads an expression back into the form; shapes the form cannot build come back as custom. */
export function fromCron(expr: string): ScheduleForm {
  const custom = { ...defaultSchedule, kind: "custom" as const, custom: expr };
  const f = expr.trim().split(/\s+/);
  if (f.length !== 5) return custom;
  const [mi = "", h = "", dom = "", mon = "", dow = ""] = f;
  if (mon !== "*") return custom;
  if (mi === "*" && h === "*" && dom === "*" && dow === "*") return { ...defaultSchedule, kind: "minute" };
  const step = /^\*\/(\d+)$/.exec(mi);
  if (step && h === "*" && dom === "*" && dow === "*" && everyOptions.includes(Number(step[1]))) return { ...defaultSchedule, kind: "every", every: Number(step[1]) };
  const minute = num(mi, 0, 59);
  if (minute === null) return custom;
  if (h === "*" && dom === "*" && dow === "*") return { ...defaultSchedule, kind: "hourly", minute };
  const hour = num(h, 0, 23);
  if (hour === null) return custom;
  if (dom === "*" && dow === "*") return { ...defaultSchedule, kind: "daily", minute, hour };
  const weekday = num(dow, 0, 6);
  if (dom === "*" && weekday !== null) return { ...defaultSchedule, kind: "weekly", minute, hour, weekday };
  const day = num(dom, 1, 28);
  if (dow === "*" && day !== null) return { ...defaultSchedule, kind: "monthly", minute, hour, day };
  return custom;
}

const pad = (n: number) => String(n).padStart(2, "0");

export const weekdayNames = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];

/** A sentence for the shapes the form knows, the expression itself otherwise. */
export function describeSchedule(expr: string, t: TFunction): string {
  const f = fromCron(expr);
  const time = `${pad(f.hour)}:${pad(f.minute)}`;
  switch (f.kind) {
    case "minute":
      return t("every minute");
    case "every":
      return t("every {{minutes}} minutes", { minutes: f.every });
    case "hourly":
      return t("hourly at minute {{minute}}", { minute: f.minute });
    case "daily":
      return t("daily at {{time}}", { time });
    case "weekly":
      return t("every {{weekday}} at {{time}}", { weekday: t(weekdayNames[f.weekday] ?? ""), time });
    case "monthly":
      return t("monthly on day {{day}} at {{time}}", { day: f.day, time });
    case "custom":
      return expr;
  }
}
