import { Component, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Button, ErrorState } from "./ui";

/**
 * Catches a rendering error of the page below it, so one broken section shows an error
 * instead of a blank screen. The shell keys it by the path: navigating elsewhere starts
 * over.
 */
export class PageErrorBoundary extends Component<{ children: ReactNode }, { error: Error | null }> {
  state = { error: null as Error | null };

  static getDerivedStateFromError(error: Error) {
    return { error };
  }

  render() {
    return this.state.error ? <PageError message={this.state.error.message} /> : this.props.children;
  }
}

function PageError({ message }: { message: string }) {
  const { t } = useTranslation();
  return (
    <ErrorState
      title={t("This page ran into an error")}
      message={message}
      action={
        <Button variant="secondary" onClick={() => window.location.reload()}>
          {t("Reload the page")}
        </Button>
      }
    />
  );
}
