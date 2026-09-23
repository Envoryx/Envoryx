import { Folder, Save } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { useSettings, useUpdateSettings } from "@/api/hooks";
import { Alert, Button, Card, CardHeader, ErrorState, Field, Input, Spinner } from "@/components/ui";
import { errorText } from "@/lib/errors";

/** Folder of the FolderView3 plugin the containers are sorted into on Unraid's Docker page. */
export function UnraidCard() {
  const { t } = useTranslation();
  const s = useSettings();
  const update = useUpdateSettings();
  const current = s.data?.folderViewFolder ?? "";
  const [folder, setFolder] = useState(current);
  const [msg, setMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  useEffect(() => setFolder(current), [current]);

  function submit(e: FormEvent) {
    e.preventDefault();
    setMsg(null);
    update.mutate(
      { folderViewFolder: folder.trim() },
      {
        onSuccess: () => setMsg({ tone: "green", text: t("Saved. Projects move into the folder the next time they are started.") }),
        onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Saving failed")) }),
      },
    );
  }

  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <Folder className="size-4 text-accent-500" aria-hidden />
            {t("Unraid Docker page")}
          </span>
        }
        description={t("Envoryx containers carry the Envoryx icon. With the FolderView3 plugin they can also be sorted into a folder automatically.")}
      />
      {s.isPending ? (
        <div className="p-5">
          <Spinner />
        </div>
      ) : s.isError ? (
        <div className="p-5">
          <ErrorState message={errorText(s.error, t)} />
        </div>
      ) : (
        <form onSubmit={submit} className="space-y-4 p-5">
          {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
          <Field label={t("FolderView3 folder")} htmlFor="folderview-folder" hint={t("Create a folder with exactly this name in FolderView3 first. Leave empty for no label – a folder with the regex ^envoryx- collects the containers as well.")}>
            <Input id="folderview-folder" value={folder} onChange={(e) => setFolder(e.target.value)} placeholder="Envoryx" maxLength={64} spellCheck={false} />
          </Field>
          <Button type="submit" variant="primary" loading={update.isPending} disabled={folder.trim() === current} icon={<Save className="size-4" />}>
            {t("Save")}
          </Button>
        </form>
      )}
    </Card>
  );
}
