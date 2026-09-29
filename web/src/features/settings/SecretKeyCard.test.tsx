import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SecretKeyCard } from "./SecretKeyCard";
import { authedRoutes, mockApi, renderApp } from "@/test/utils";

const fileKey = { source: "file", path: "/config/secret.key", keyId: "aaaa1111", canRotate: true, envKey: "ENVORYX_SECRET_KEY", envOldKey: "ENVORYX_SECRET_KEY_OLD" };

describe("SecretKeyCard", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("shows, reveals and replaces a file key", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /settings/secret-key": () => ({ body: { secretKey: fileKey } }),
      "POST /settings/secret-key/reveal": () => ({ body: { key: "c2VjcmV0LWtleQ==" } }),
      "POST /settings/secret-key/rotate": () => ({ body: { secretKey: { ...fileKey, keyId: "cccc3333" }, resealed: { database: 7, files: 2, backups: 3 } } }),
    });
    renderApp(<SecretKeyCard />);
    const user = userEvent.setup();
    expect(await screen.findByText("aaaa1111")).toBeInTheDocument();
    expect(screen.getByText("/config/secret.key")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Show key" }));
    await waitFor(() => expect(api.calls.some((c) => c.url.endsWith("/reveal"))).toBe(true));
    await user.click(screen.getByRole("button", { name: "Replace the key" }));
    await user.click(screen.getAllByRole("button", { name: "Replace the key" }).at(-1)!);
    expect(await screen.findByText(/New key cccc3333 in place: 7 database values, 2 files and 3 project backups/)).toBeInTheDocument();
    expect(screen.getByText("cccc3333")).toBeInTheDocument();
  });

  it("explains how to change a key from the environment", async () => {
    mockApi({ ...authedRoutes, "GET /settings/secret-key": () => ({ body: { secretKey: { ...fileKey, source: "env", path: undefined, canRotate: false } } }) });
    renderApp(<SecretKeyCard />);
    expect(await screen.findByText(/set a new one as ENVORYX_SECRET_KEY and the current one as ENVORYX_SECRET_KEY_OLD/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Replace the key" })).not.toBeInTheDocument();
  });
});
