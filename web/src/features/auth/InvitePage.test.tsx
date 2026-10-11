import { screen } from "@testing-library/react";
import { Route, Routes } from "react-router-dom";
import { InvitePage } from "./InvitePage";
import { mockApi, renderApp } from "@/test/utils";

const routes = (
  <Routes>
    <Route path="/invite/:token" element={<InvitePage />} />
    <Route path="/login" element={<h1>Sign in</h1>} />
  </Routes>
);

describe("InvitePage", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("explains an unusable link and leads to the sign-in page", async () => {
    mockApi({
      "GET /setup": () => ({ body: { needsSetup: false } }),
      "GET /auth/me": () => ({ status: 401, body: { error: { code: "unauthorized", message: "not signed in" } } }),
      "GET /auth/oidc": () => ({ body: { enabled: false } }),
      "GET /invites/": () => ({ status: 410, body: { error: { code: "invite_invalid", message: "this invitation link is invalid or has expired; ask an admin for a new one" } } }),
    });
    renderApp(routes, { route: "/invite/doesnotexist" });
    expect(await screen.findByRole("heading", { name: "This invitation link cannot be used" })).toBeInTheDocument();
    expect(screen.queryByText("Welcome to Envoryx")).not.toBeInTheDocument();
    expect(screen.queryByText(/ask an admin for a new one/)).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Go to sign-in" })).toHaveAttribute("href", "/login");
  });
});
