import { act, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { OperationsTray, OperationHint } from "./OperationsTray";
import type { Operation } from "@/api/types";
import i18n from "@/i18n";
import { authedRoutes, mockApi, renderApp } from "@/test/utils";

const now = new Date().toISOString();
const base: Operation = { id: "op1", projectId: "p1", projectSlug: "acme-shop", projectName: "Acme Shop", action: "start", startedAt: now, updatedAt: now };

describe("OperationsTray", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("shows the running step, then the outcome, and lets failures be dismissed", async () => {
    let ops: Operation[] = [{ ...base, step: "Pulling the image {{image}}: {{status}}", stepArgs: { image: "caddy:2-alpine", status: "downloading 42% (10 MB of 24 MB)" } }];
    mockApi({ ...authedRoutes, "GET /operations": () => ({ body: { operations: ops } }) });
    renderApp(<OperationsTray />);

    expect(await screen.findByText("Starting Acme Shop")).toBeInTheDocument();
    expect(screen.getByText(/Pulling the image caddy:2-alpine: downloading 42%/)).toBeInTheDocument();

    ops = [{ ...base, finishedAt: new Date().toISOString(), error: "start container envoryx-acme-shop-php: boom" }];
    // The tray polls every second while something runs.
    expect(await screen.findByText("Starting Acme Shop failed", {}, { timeout: 3000 })).toBeInTheDocument();
    expect(screen.getByText(/boom/)).toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole("button", { name: "Dismiss" }));
    expect(screen.queryByText("Starting Acme Shop failed")).not.toBeInTheDocument();
  });

  it("does not announce operations that were already over on page load", async () => {
    mockApi({ ...authedRoutes, "GET /operations": () => ({ body: { operations: [{ ...base, finishedAt: now }] } }) });
    renderApp(<OperationsTray />);
    await act(async () => {
      await new Promise((r) => setTimeout(r, 50));
    });
    expect(screen.queryByText("Acme Shop started")).not.toBeInTheDocument();
  });

  it("announces only the user's own operations and Envoryx's own to an admin", async () => {
    const ops: Operation[] = [
      { ...base, id: "a", projectName: "Mine", userId: "u1" },
      { ...base, id: "b", projectName: "Theirs", userId: "u2" },
      { ...base, id: "c", projectName: "Scheduled" },
    ];
    const me = (role: string) => ({ body: { user: { id: "u1", username: "someone", role }, admin: role === "admin", projectRoles: {} } });
    mockApi({ ...authedRoutes, "GET /auth/me": () => me("developer"), "GET /operations": () => ({ body: { operations: ops } }) });
    const { unmount } = renderApp(<OperationsTray />);
    expect(await screen.findByText("Starting Mine")).toBeInTheDocument();
    expect(screen.queryByText("Starting Theirs")).not.toBeInTheDocument();
    expect(screen.queryByText("Starting Scheduled")).not.toBeInTheDocument();
    unmount();

    mockApi({ ...authedRoutes, "GET /auth/me": () => me("admin"), "GET /operations": () => ({ body: { operations: ops } }) });
    renderApp(<OperationsTray />);
    expect(await screen.findByText("Starting Scheduled")).toBeInTheDocument();
    expect(screen.getByText("Starting Mine")).toBeInTheDocument();
    expect(screen.queryByText("Starting Theirs")).not.toBeInTheDocument();
  });

  it("renders the inline hint with verb and translated step", () => {
    renderApp(<OperationHint op={{ ...base, action: "update", step: "Recreating the container {{name}}", stepArgs: { name: "envoryx-acme-shop-php" } }} />);
    expect(screen.getByTestId("operation-hint")).toHaveTextContent("Applying settings… · Recreating the container envoryx-acme-shop-php");
  });

  it("translates the progress of an image pull inside the step", async () => {
    await i18n.changeLanguage("de");
    try {
      renderApp(<OperationHint op={{ ...base, step: "Pulling the image {{image}}: {{status}}", stepArgs: { image: "caddy:2-alpine", status: "contacting the registry" } }} />);
      const hint = screen.getByTestId("operation-hint");
      expect(hint).toHaveTextContent(i18n.t("contacting the registry"));
      expect(hint).not.toHaveTextContent("contacting the registry");
    } finally {
      await i18n.changeLanguage("en");
    }
  });
});
