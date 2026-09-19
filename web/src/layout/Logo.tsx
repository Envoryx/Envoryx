import logoSvg from "./envoryx-logo.svg?raw";

/** The <E> mark (2:1). `size` is its height. Chevrons carry the accent, the bars the foreground; both follow the theme tokens. */
export function LogoMark({ size = 20, className = "" }: { size?: number; className?: string }) {
  return (
    <svg width={size * 2} height={size} viewBox="0 0 396 198" aria-hidden className={`shrink-0 ${className}`}>
      <path fill="var(--logo-accent)" d="M87 1h6v39L34 99l59 59v39h-6L0 110V88Z" />
      <path fill="var(--logo-accent)" d="M308 1h-6v39l59 59-59 59v39h6l87-87V88Z" />
      <path fill="var(--logo-fg)" d="M156 0h107v33H132v-9ZM132 82h79v33h-79ZM132 164h131v33H132Z" />
    </svg>
  );
}

export function Logo({ size = 18, withText = false }: { size?: number; withText?: boolean }) {
  return (
    <span className="inline-flex items-center gap-2.5 text-fg">
      <LogoMark size={size} />
      {withText && (
        <span className="text-[15px] font-semibold tracking-tight">
          <span className="text-accent-600 dark:text-accent-400">Env</span>oryx
        </span>
      )}
    </span>
  );
}

/** Mark, wordmark and tagline – the full logo (envoryx-logo.svg), inlined so it picks up the accent and theme. */
export function LogoFull({ className = "" }: { className?: string }) {
  return <span className={`inline-block [&>svg]:h-auto [&>svg]:w-full ${className}`} role="img" aria-label="Envoryx – build deeper" dangerouslySetInnerHTML={{ __html: logoSvg }} />;
}
