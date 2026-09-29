import { clsx } from "clsx";
import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link, useNavigate } from "react-router-dom";
import { Select } from "./ui";

export interface Section<Id extends string> {
  id: Id;
  label: string;
  icon: LucideIcon;
  badge?: ReactNode;
}

export interface SectionGroup<Id extends string> {
  group?: string;
  items: Section<Id>[];
}

/**
 * A page split into sections: a grouped sidebar on wide screens, a select on phones, and the
 * chosen section next to it. Labels and group names are translation keys.
 */
export function SectionLayout<Id extends string>({ label, groups, current, href, children }: { label: string; groups: SectionGroup<Id>[]; current: Id; href: (id: Id) => string; children: ReactNode }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const option = (i: Section<Id>) => (
    <option key={i.id} value={i.id}>
      {t(i.label)}
    </option>
  );
  return (
    <div className="lg:flex lg:items-start lg:gap-8">
      <nav aria-label={label} className="hidden w-48 shrink-0 lg:sticky lg:top-6 lg:block">
        {groups.map((s) => (
          <div key={s.group ?? ""} className="mb-4">
            {s.group && <p className="mb-1 px-2 text-[11px] font-semibold uppercase tracking-wider text-subtle">{t(s.group)}</p>}
            <ul className="space-y-0.5">
              {s.items.map((i) => (
                <li key={i.id}>
                  <Link
                    to={href(i.id)}
                    aria-current={current === i.id ? "page" : undefined}
                    className={clsx("flex items-center gap-2.5 rounded-md px-2 py-1.5 text-sm font-medium transition-colors", current === i.id ? "bg-muted text-fg" : "text-muted hover:bg-muted hover:text-fg")}
                  >
                    <i.icon className="size-4 shrink-0" aria-hidden />
                    <span className="min-w-0 flex-1">{t(i.label)}</span>
                    {i.badge}
                  </Link>
                </li>
              ))}
            </ul>
          </div>
        ))}
      </nav>
      <div className="mb-4 lg:hidden">
        <Select aria-label={label} value={current} onChange={(e) => navigate(href(e.target.value as Id))}>
          {groups.map((s) =>
            s.group ? (
              <optgroup key={s.group} label={t(s.group)}>
                {s.items.map(option)}
              </optgroup>
            ) : (
              s.items.map(option)
            ),
          )}
        </Select>
      </div>
      <div className="min-w-0 flex-1">{children}</div>
    </div>
  );
}
