import { act, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { OperationsTray, OperationHint } from "./OperationsTray";
import type { Operation } from "@/api/types";
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

  it("renders the inline hint with verb and translated step", () => {
    renderApp(<OperationHint op={{ ...base, action: "update", step: "Recreating the container {{name}}", stepArgs: { name: "envoryx-acme-shop-php" } }} />);
    expect(screen.getByTestId("operation-hint")).toHaveTextContent("Applying settings… · Recreating the container envoryx-acme-shop-php");
  });
});
