import logoSvg from "./envoryx-logo.svg?raw";

// The logo is ASCII art, the brand frame of the Foundry film; scripts/logo/build.mjs writes
// the SVG. It has two colours, the accent (--logo-accent, the bright tone of the chosen
// accent colour) and white, always on its dark ground.

/**
 * The <E> mark at small sizes, where the ASCII picture would blur: its three characters on
 * the dark ground. `size` is the height of the characters.
 */
export function LogoMark({ size = 20, className = "" }: { size?: number; className?: string }) {
  return (
    <span
      aria-hidden
      className={`inline-flex shrink-0 items-center rounded-md font-mono font-bold leading-none tracking-tight ${className}`}
      style={{ fontSize: size * 0.8, padding: `${size * 0.22}px ${size * 0.3}px`, background: "#06070a" }}
    >
      <span style={{ color: "var(--logo-accent)" }}>&lt;</span>
      <span style={{ color: "#f0f1f2" }}>E</span>
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

/** Mark, wordmark and tagline: the full logo on its dark ground. */
export function LogoFull({ className = "" }: { className?: string }) {
  return <span className={`inline-block [&>svg]:h-auto [&>svg]:w-full ${className}`} role="img" aria-label="Envoryx - build deeper" dangerouslySetInnerHTML={{ __html: logoSvg }} />;
}
