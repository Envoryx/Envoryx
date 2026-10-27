import logoSvg from "./envoryx-logo.svg?raw";

// The logo is ASCII art, the brand frame of the Foundry film; scripts/logo/build.mjs writes
// the SVG. It has two colours, the accent (--logo-accent) and the text colour (--logo-fg),
// and no ground of its own: both follow the theme, set in index.css.

/**
 * The <E> mark at small sizes, where the ASCII picture would blur: its three characters on
 * the page. `size` is the height of the characters.
 */
export function LogoMark({ size = 20, className = "" }: { size?: number; className?: string }) {
  return (
    <span
      aria-hidden
      className={`inline-flex shrink-0 items-center font-mono font-bold leading-none tracking-tight ${className}`}
      style={{ fontSize: size * 0.8 }}
    >
      <span style={{ color: "var(--logo-accent)" }}>&lt;</span>
      <span style={{ color: "var(--logo-fg)" }}>E</span>
      <span style={{ color: "var(--logo-accent)" }}>&gt;</span>
    </span>
  );
}

export function Logo({ size = 18, withText = false }: { size?: number; withText?: boolean }) {
  return (
    <span className="inline-flex items-center gap-2.5 text-fg">
      <LogoMark size={size} />
      {withText && (
        <span className="font-mono text-[15px] font-semibold tracking-tight">Envoryx</span>
      )}
    </span>
  );
}

/** Mark, wordmark and tagline: the full logo. */
export function LogoFull({ className = "" }: { className?: string }) {
  return <span className={`inline-block [&>svg]:h-auto [&>svg]:w-full ${className}`} role="img" aria-label="Envoryx - build deeper" dangerouslySetInnerHTML={{ __html: logoSvg }} />;
}
