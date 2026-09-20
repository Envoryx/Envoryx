import { useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import { ExternalLink } from "lucide-react";
import { useSettings } from "@/api/hooks";
import { formatVersion } from "@/lib/format";
import { mountFilm } from "./film";
import "./foundry.css";

const REPO = "https://github.com/envoryx/envoryx";

/** The spatial foundry – a 38-second ASCII short film with the version and project links below, reached through the logo. */
export function FoundryPage() {
  return (
    <div>
      <Film />
      <About />
    </div>
  );
}

function About() {
  const { t } = useTranslation();
  const s = useSettings();
  const links = [
    { label: t("Documentation"), href: `${REPO}/blob/main/DEPLOYMENT.md` },
    { label: t("Release notes"), href: s.data?.update?.url ?? `${REPO}/blob/main/CHANGELOG.md` },
    { label: t("Source code"), href: REPO },
    { label: t("License"), href: `${REPO}/blob/main/LICENSE` },
  ];
  return (
    <div className="mt-4 flex flex-wrap items-center justify-between gap-x-6 gap-y-2 px-1 text-xs text-muted">
      <span>
        <span className="font-medium text-fg">Envoryx</span>
        {s.data && <> {formatVersion(t, s.data.version, s.data.update)}</>}
        <span className="text-subtle"> · AGPL-3.0</span>
      </span>
      <nav className="flex flex-wrap gap-x-4 gap-y-1" aria-label={t("About Envoryx")}>
        {links.map((l) => (
          <a key={l.href} href={l.href} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 hover:text-fg hover:underline">
            <ExternalLink className="size-3" aria-hidden />
            {l.label}
          </a>
        ))}
      </nav>
    </div>
  );
}

/** The markup and ids are what film.js expects. */
function Film() {
  const root = useRef<HTMLDivElement>(null);
  useEffect(() => (root.current ? mountFilm(root.current) : undefined), []);

  return (
    <div ref={root} className="foundry">
      <section className="movie" id="movie" aria-label="The spatial foundry – animated ASCII short film">
        <canvas id="world" role="img" aria-label="Perspective ASCII factory with two conveyor belts, spatial Docker containers and synchronised assembly arms." />
        <div className="scene-header">
          <div>
            <div className="eyebrow" id="chapterTag">01 / INITIALIZE</div>
            <h1 className="scene-name" id="sceneName">It starts with a cursor.</h1>
          </div>
          <div className="scene-note">
            ENVORYX EXPLORER
            <br />
            <span id="coordinates">LOCALHOST / SECTOR 001</span>
          </div>
        </div>
        <div className="scene-bottom">
          <span>
            <i className="signal" aria-hidden />
            <span id="status">SYSTEM ONLINE</span>
          </span>
          <span id="terminalLine">
            &gt; envoryx init<span className="mint"> _</span>
          </span>
          <button className="fs" id="fullscreen" aria-label="Toggle fullscreen" title="Fullscreen (F)">
            [ + ]
          </button>
        </div>
      </section>
      <div className="transport">
        <div className="controls">
          <button className="play" id="play" aria-label="Pause animation">
            II PAUSE
          </button>
          <button className="replay" id="replay" aria-label="Restart animation" title="Restart (R)">
            ↺
          </button>
        </div>
        <nav className="timeline" aria-label="Chapters" id="chapters" />
        <div className="time">
          <span id="clock">00:00</span> <span style={{ color: "#40594c" }}>/</span> 00:38
        </div>
      </div>
      <div className="reduced" id="reduced">
        Reduced motion is on. The scene holds a still frame; start it deliberately or pick a chapter.
      </div>
      <p className="sr-only" id="announcement" aria-live="polite" />
    </div>
  );
}
