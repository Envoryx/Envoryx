// Applies the stored theme and accent before React renders. It lives in a
// module (not an inline <script> in index.html) because the server's CSP
// forbids inline scripts; imported first from main.tsx it still runs before
// the first paint of the app.
try {
  const t = localStorage.getItem("envoryx.theme");
  const dark = t === "dark" || (t !== "light" && matchMedia("(prefers-color-scheme: dark)").matches);
  document.documentElement.classList.toggle("dark", dark);
  const a = localStorage.getItem("envoryx.accent");
  if (a && a !== "mint") document.documentElement.dataset.accent = a;
} catch {
  /* storage unavailable: system theme, default accent */
}
