import { Plus } from "lucide-react";
import { useTranslation } from "react-i18next";
import { usePlanLimitReached } from "@/api/hooks";
import { Button, LinkButton } from "@/components/ui";

/** The New project button; at the plan's project limit it is disabled and says why. */
export function NewProjectButton() {
  const { t } = useTranslation();
  const full = usePlanLimitReached("projects");
  if (full) {
    return (
      <span className="flex flex-wrap items-center justify-end gap-2">
        <span className="text-xs text-muted">{t("The plan's project limit is reached.")}</span>
        <Button variant="primary" icon={<Plus className="size-4" />} disabled title={t("Delete a project or ask your hoster for a larger plan.")}>
          {t("New project")}
        </Button>
      </span>
    );
  }
  return (
    <LinkButton to="/projects/new" variant="primary" icon={<Plus className="size-4" />}>
      {t("New project")}
    </LinkButton>
  );
}
