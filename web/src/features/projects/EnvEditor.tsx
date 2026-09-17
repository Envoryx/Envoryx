import { Eye, EyeOff, Plus, Trash2 } from "lucide-react";
import type { EnvVar } from "@/api/types";
import { Button, Input } from "@/components/ui";

export function EnvEditor({ value, onChange }: { value: EnvVar[]; onChange: (next: EnvVar[]) => void }) {
  const update = (i: number, patch: Partial<EnvVar>) => onChange(value.map((e, idx) => (idx === i ? { ...e, ...patch } : e)));
  const remove = (i: number) => onChange(value.filter((_, idx) => idx !== i));
  const add = () => onChange([...value, { key: "", value: "", isSecret: false }]);

  return (
    <div className="space-y-2">
      {value.length === 0 && <p className="text-sm text-subtle">No environment variables. They are injected into every project container.</p>}
      {value.map((e, i) => (
        <div key={i} className="flex items-center gap-2">
          <Input
            aria-label="Variable name"
            placeholder="APP_ENV"
            value={e.key}
            onChange={(ev) => update(i, { key: ev.target.value.toUpperCase().replace(/[^A-Z0-9_]/g, "") })}
            className="w-44 font-mono text-xs"
            spellCheck={false}
          />
          <Input
            aria-label="Variable value"
            placeholder="value"
            type={e.isSecret ? "password" : "text"}
            value={e.value}
            onChange={(ev) => update(i, { value: ev.target.value })}
            className="font-mono text-xs"
            spellCheck={false}
            autoComplete="off"
          />
          <Button variant="ghost" size="sm" onClick={() => update(i, { isSecret: !e.isSecret })} title={e.isSecret ? "Secret (masked)" : "Mark as secret"} aria-label="Toggle secret">
            {e.isSecret ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
          </Button>
          <Button variant="ghost" size="sm" onClick={() => remove(i)} aria-label="Remove variable">
            <Trash2 className="size-4" />
          </Button>
        </div>
      ))}
      <Button size="sm" onClick={add} icon={<Plus className="size-3.5" />}>
        Add variable
      </Button>
    </div>
  );
}
