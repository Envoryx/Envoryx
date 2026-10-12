import { clsx } from "clsx";
import { Download } from "lucide-react";
import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import { useProjectActions } from "@/api/hooks";
import type { Project } from "@/api/types";
import { Badge, Button } from "@/components/ui";
import { translateMessage } from "@/lib/errors";
import { projectAccess } from "@/lib/access";
import { useLiveRun } from "./useLiveRun";

/** The catalogue action that runs Microsoft's install script in the .NET container. */
const VSDBG_ACTION = "dotnet:vsdbg";

/** Microsoft's licence for vsdbg: Microsoft's tools only. */
const VSDBG_LICENSE = "https://aka.ms/VSCode-DotNet-DbgLicense";

/**
 * vsdbg for VS Code: whether it is in the project home, and a button that downloads it
 * from Microsoft (or updates it) with the output shown below.
 */
export function VsdbgInstall({ project: p, manualCommand }: { project: Project; manualCommand: string }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const can = projectAccess(p);
  const version = useQuery({ queryKey: ["projects", p.id, "vsdbg"], queryFn: async () => (await api.projects.vsdbg(p.id)).version });
  const actions = useProjectActions(p.id);
  const action = actions.data?.find((a) => a.id === VSDBG_ACTION);
  const live = useLiveRun();
  const state = live.run?.state;

  // A finished run changed what is installed.
  useEffect(() => {
    if (state === "finished" || state === "failed") void qc.invalidateQueries({ queryKey: ["projects", p.id, "vsdbg"] });
  }, [state, p.id, qc]);

  const installed = version.data ? version.data : "";
  const unavailable = action && !action.available ? translateMessage(action.reason, t) : "";

  return (
    <div className="mt-3">
      <p className="text-xs text-muted">
        {t("VS Code with the C# extension (it needs a .NET SDK on your computer) debugs with Microsoft's vsdbg. Its licence allows it only with Microsoft's tools, so Envoryx doesn't ship it; the button downloads it from Microsoft into the project home, where it stays.")}{" "}
        <a className="text-accent-600 hover:underline" href={VSDBG_LICENSE} target="_blank" rel="noreferrer">
          {t("License")}
        </a>
      </p>
      <div className="mt-2 flex flex-wrap items-center gap-2">
        {version.isPending ? null : installed ? (
          <Badge tone="green">{t("vsdbg {{version}} installed", { version: installed })}</Badge>
        ) : (
          <Badge tone="gray">{t("vsdbg not installed")}</Badge>
        )}
        {can.operate && (
          <Button
            size="sm"
            icon={<Download className="size-3.5" />}
            disabled={!action?.available || state === "running"}
            title={unavailable || undefined}
            onClick={() => action && live.start(`/projects/${encodeURIComponent(p.id)}/actions/${encodeURIComponent(action.id)}/ws`, manualCommand)}
          >
            {state === "running" ? t("Downloading…") : installed ? t("Update vsdbg") : t("Install vsdbg")}
          </Button>
        )}
        {unavailable && <span className="text-xs text-subtle">{unavailable}</span>}
      </div>
      <div ref={live.host} className={clsx("mt-2 h-48 overflow-hidden rounded-md bg-[#0f1115] p-2", !live.run && "hidden")} data-testid="vsdbg-output" />
      <p className="mt-2 text-xs text-subtle">{t("Or in the .NET terminal:")}</p>
      <pre className="mt-1 overflow-x-auto rounded-md bg-muted p-2 font-mono text-[11px]">{manualCommand}</pre>
    </div>
  );
}
