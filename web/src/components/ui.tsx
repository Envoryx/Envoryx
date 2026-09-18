import { clsx } from "clsx";
import { Loader2, AlertTriangle, Inbox } from "lucide-react";
import { forwardRef, useEffect, useRef, type ButtonHTMLAttributes, type InputHTMLAttributes, type ReactNode, type SelectHTMLAttributes } from "react";
import { Link, type LinkProps } from "react-router-dom";

/* ---------- Button ---------- */

type Variant = "primary" | "secondary" | "ghost" | "danger";
type Size = "sm" | "md";

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant;
  size?: Size;
  loading?: boolean;
  icon?: ReactNode;
}

const variantClasses: Record<Variant, string> = {
  primary: "bg-accent-600 text-white hover:bg-accent-500 disabled:hover:bg-accent-600 shadow-sm",
  secondary: "bg-elevated border border-default text-fg hover:bg-muted",
  ghost: "text-muted hover:bg-muted hover:text-fg",
  danger: "bg-red-600 text-white hover:bg-red-500 shadow-sm",
};

const sizeClasses: Record<Size, string> = {
  sm: "h-8 px-2.5 text-xs gap-1.5",
  md: "h-9 px-3.5 text-sm gap-2",
};

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = "secondary", size = "md", loading = false, icon, className, children, disabled, ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      className={clsx(
        "inline-flex items-center justify-center rounded-md font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed whitespace-nowrap",
        variantClasses[variant],
        sizeClasses[size],
        className,
      )}
      disabled={disabled || loading}
      {...rest}
    >
      {loading ? <Loader2 className="size-4 animate-spin" aria-hidden /> : icon}
      {children}
    </button>
  );
});

/** A router link styled like a button (avoids nesting interactive elements). */
export function LinkButton({
  variant = "secondary",
  size = "md",
  icon,
  className,
  children,
  ...rest
}: LinkProps & { variant?: Variant; size?: Size; icon?: ReactNode; className?: string }) {
  return (
    <Link
      className={clsx(
        "inline-flex items-center justify-center rounded-md font-medium transition-colors whitespace-nowrap",
        variantClasses[variant],
        sizeClasses[size],
        className,
      )}
      {...rest}
    >
      {icon}
      {children}
    </Link>
  );
}

/* ---------- Inputs ---------- */

const fieldClasses =
  "w-full h-9 rounded-md border border-default bg-elevated px-3 text-sm text-fg placeholder:text-subtle focus:border-accent-500 focus:outline-none focus:ring-2 focus:ring-accent-500/30 disabled:opacity-60";

export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(function Input(
  { className, ...rest },
  ref,
) {
  return <input ref={ref} className={clsx(fieldClasses, className)} {...rest} />;
});

export const Select = forwardRef<HTMLSelectElement, SelectHTMLAttributes<HTMLSelectElement>>(function Select(
  { className, children, ...rest },
  ref,
) {
  return (
    <select ref={ref} className={clsx(fieldClasses, "pr-8", className)} {...rest}>
      {children}
    </select>
  );
});

export function Field({
  label,
  hint,
  error,
  htmlFor,
  children,
}: {
  label: string;
  hint?: string | undefined;
  error?: string | undefined;
  htmlFor?: string | undefined;
  children: ReactNode;
}) {
  return (
    <div className="space-y-1.5">
      <label htmlFor={htmlFor} className="block text-sm font-medium text-fg">
        {label}
      </label>
      {children}
      {error ? (
        <p className="text-xs text-red-500" role="alert">
          {error}
        </p>
      ) : hint ? (
        <p className="text-xs text-subtle">{hint}</p>
      ) : null}
    </div>
  );
}

export function Checkbox({
  label,
  description,
  ...rest
}: InputHTMLAttributes<HTMLInputElement> & { label: string; description?: string | undefined }) {
  return (
    <label className="flex items-start gap-3 cursor-pointer">
      <input type="checkbox" className="mt-0.5 size-4 rounded border-default accent-accent-600" {...rest} />
      <span>
        <span className="block text-sm font-medium text-fg">{label}</span>
        {description && <span className="block text-xs text-subtle">{description}</span>}
      </span>
    </label>
  );
}

/* ---------- Layout primitives ---------- */

export function Card({ className, children, ...rest }: { className?: string; children: ReactNode } & React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div className={clsx("rounded-xl border border-default bg-elevated shadow-sm", className)} {...rest}>
      {children}
    </div>
  );
}

export function CardHeader({ title, description, actions }: { title: ReactNode; description?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="flex items-start justify-between gap-4 border-b border-default px-5 py-4">
      <div>
        <h2 className="text-sm font-semibold text-fg">{title}</h2>
        {description && <p className="mt-0.5 text-xs text-muted">{description}</p>}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </div>
  );
}

export function PageHeader({ title, description, actions }: { title: ReactNode; description?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="mb-6 flex flex-wrap items-end justify-between gap-4">
      <div>
        <h1 className="text-xl font-semibold tracking-tight text-fg">{title}</h1>
        {description && <p className="mt-1 text-sm text-muted">{description}</p>}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </div>
  );
}

/* ---------- Status ---------- */

export type Tone = "green" | "gray" | "amber" | "red" | "blue";

const toneDot: Record<Tone, string> = {
  green: "bg-emerald-500",
  gray: "bg-zinc-400",
  amber: "bg-amber-500",
  red: "bg-red-500",
  blue: "bg-accent-500",
};

const toneBadge: Record<Tone, string> = {
  green: "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 ring-emerald-500/20",
  gray: "bg-zinc-500/10 text-zinc-600 dark:text-zinc-300 ring-zinc-500/20",
  amber: "bg-amber-500/10 text-amber-600 dark:text-amber-400 ring-amber-500/20",
  red: "bg-red-500/10 text-red-600 dark:text-red-400 ring-red-500/20",
  blue: "bg-accent-500/10 text-accent-600 dark:text-accent-300 ring-accent-500/20",
};

export function StatusDot({ tone, pulse = false, className }: { tone: Tone; pulse?: boolean; className?: string }) {
  return (
    <span className={clsx("relative inline-flex size-2.5", className)} aria-hidden>
      {pulse && <span className={clsx("absolute inline-flex h-full w-full animate-ping rounded-full opacity-60", toneDot[tone])} />}
      <span className={clsx("relative inline-flex size-2.5 rounded-full", toneDot[tone])} />
    </span>
  );
}

export function Badge({ tone = "gray", children, className }: { tone?: Tone; children: ReactNode; className?: string }) {
  return (
    <span className={clsx("inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-[11px] font-medium ring-1 ring-inset", toneBadge[tone], className)}>
      {children}
    </span>
  );
}

/* ---------- States ---------- */

export function Spinner({ label = "Loading…" }: { label?: string }) {
  return (
    <div className="flex items-center gap-2 py-10 justify-center text-sm text-muted" role="status">
      <Loader2 className="size-4 animate-spin" aria-hidden />
      {label}
    </div>
  );
}

export function ErrorState({ title = "Something went wrong", message, action }: { title?: string; message?: string | undefined; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-2 rounded-xl border border-red-500/30 bg-red-500/5 px-6 py-8 text-center" role="alert">
      <AlertTriangle className="size-6 text-red-500" aria-hidden />
      <p className="text-sm font-medium text-fg">{title}</p>
      {message && <p className="max-w-md text-sm text-muted">{message}</p>}
      {action}
    </div>
  );
}

export function EmptyState({ title, message, action }: { title: string; message?: string; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-2 rounded-xl border border-dashed border-default px-6 py-12 text-center">
      <Inbox className="size-6 text-subtle" aria-hidden />
      <p className="text-sm font-medium text-fg">{title}</p>
      {message && <p className="max-w-md text-sm text-muted">{message}</p>}
      {action && <div className="mt-2">{action}</div>}
    </div>
  );
}

export function Alert({ tone = "amber", title, children }: { tone?: Tone; title?: string; children: ReactNode }) {
  return (
    <div className={clsx("rounded-lg px-4 py-3 text-sm ring-1 ring-inset", toneBadge[tone])} role={tone === "red" ? "alert" : "status"}>
      {title && <p className="font-medium">{title}</p>}
      <div className={clsx(title && "mt-1", "text-fg/80")}>{children}</div>
    </div>
  );
}

/* ---------- Dialog (native <dialog>) ---------- */

export function Dialog({
  open,
  onClose,
  title,
  description,
  children,
  footer,
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  description?: ReactNode;
  children?: ReactNode;
  footer?: ReactNode;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    if (open && !el.open) el.showModal();
    if (!open && el.open) el.close();
  }, [open]);

  return (
    <dialog
      ref={ref}
      onClose={onClose}
      onClick={(e) => {
        if (e.target === ref.current) onClose();
      }}
      className="m-auto w-full max-w-lg rounded-xl border border-default bg-elevated p-0 text-fg shadow-2xl backdrop:bg-black/50 open:animate-in"
      aria-labelledby="dialog-title"
    >
      <div className="px-6 pt-5">
        <h2 id="dialog-title" className="text-base font-semibold">
          {title}
        </h2>
        {description && <div className="mt-1 text-sm text-muted">{description}</div>}
      </div>
      {children && <div className="px-6 py-4">{children}</div>}
      {footer && <div className="flex justify-end gap-2 border-t border-default px-6 py-4">{footer}</div>}
    </dialog>
  );
}

/* ---------- Misc ---------- */

export function Kbd({ children }: { children: ReactNode }) {
  return <kbd className="rounded border border-default bg-muted px-1.5 py-0.5 font-mono text-[11px] text-muted">{children}</kbd>;
}

export function Code({ children, className }: { children: ReactNode; className?: string }) {
  return <code className={clsx("rounded bg-muted px-1.5 py-0.5 font-mono text-[12px] text-fg", className)}>{children}</code>;
}
