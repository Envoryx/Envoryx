export function Logo({ size = 28, withText = false }: { size?: number; withText?: boolean }) {
  return (
    <span className="inline-flex items-center gap-2.5">
      <svg width={size} height={size} viewBox="0 0 32 32" aria-hidden className="shrink-0">
        <rect width="32" height="32" rx="7" fill="#6366f1" />
        <path d="M9 11h14v3H12v2h11v7H9v-3h11v-1H9z" fill="#fff" />
      </svg>
      {withText && <span className="text-[15px] font-semibold tracking-tight text-fg">Envoryx</span>}
    </span>
  );
}
