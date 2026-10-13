import { useTranslation } from "react-i18next";
import { LogOut } from "lucide-react";
import { Button } from "@/components/ui";
import { LogoFull } from "@/layout/Logo";
import { TwoFactorCard } from "@/features/settings/TwoFactorCard";
import { useAuth } from "./AuthContext";

/**
 * Shown instead of the app while the instance requires a second factor of the signed-in
 * user, who has none yet: the server answers nothing else until one is set up.
 */
export function TwoFactorSetupPage() {
  const { t } = useTranslation();
  const auth = useAuth();
  return (
    <div className="flex min-h-full items-start justify-center bg-app px-4 py-10">
      <div className="w-full max-w-2xl space-y-6">
        <div className="flex flex-col items-center gap-3 text-center">
          <LogoFull className="w-80 max-w-full" />
          <h1 className="text-lg font-semibold text-fg">{t("Set up two-factor authentication")}</h1>
          <p className="max-w-lg text-sm text-muted">
            {t("This Envoryx requires a second factor of your account. Set up an authenticator app or a passkey to continue; from then on, signing in with your password asks for it too.")}
          </p>
        </div>
        <TwoFactorCard onChanged={() => void auth.refresh()} />
        <div className="flex justify-center">
          <Button variant="ghost" icon={<LogOut className="size-4" />} onClick={() => void auth.logout()}>
            {t("Sign out")}
          </Button>
        </div>
      </div>
    </div>
  );
}
