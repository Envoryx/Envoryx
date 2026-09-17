import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router-dom";
import { LoginPage } from "./LoginPage";
import { mockApi, renderApp } from "@/test/utils";

function Home() {
  return <h1>Home</h1>;
}

describe("LoginPage", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("signs in and redirects", async () => {
    const api = mockApi({
      "GET /setup": () => ({ body: { needsSetup: false } }),
      "GET /auth/me": () => ({ status: 401, body: { error: { code: "unauthenticated", message: "authentication required" } } }),
      "POST /auth/login": (_url, init) => {
        const body = JSON.parse(init.body as string);
        if (body.password === "supersecret123") return { body: { user: { id: "u1", username: "admin", role: "admin" } } };
        return { status: 401, body: { error: { code: "invalid_credentials", message: "invalid username or password" } } };
      },
    });
    renderApp(
      <Routes>
        <Route path="/login" element={<LoginPage mode="login" />} />
        <Route path="/" element={<Home />} />
      </Routes>,
      { route: "/login" },
    );
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Username"), "admin");
    await user.type(screen.getByLabelText("Password"), "wrong-password");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByText("invalid username or password")).toBeInTheDocument();

    await user.clear(screen.getByLabelText("Password"));
    await user.type(screen.getByLabelText("Password"), "supersecret123");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByRole("heading", { name: "Home" })).toBeInTheDocument();

    const login = api.calls.find((c) => c.url.endsWith("/auth/login"));
    expect(login).toBeDefined();
    // The CSRF header must be sent on state-changing requests.
    const init = api.fetchMock.mock.calls.find((c) => (c[1]?.method ?? "GET") === "POST")?.[1];
    expect((init?.headers as Record<string, string>)["X-Requested-With"]).toBe("Staqio");
  });

  it("redirects to setup when no user exists", async () => {
    mockApi({ "GET /setup": () => ({ body: { needsSetup: true } }) });
    renderApp(
      <Routes>
        <Route path="/login" element={<LoginPage mode="login" />} />
        <Route path="/setup" element={<LoginPage mode="setup" />} />
      </Routes>,
      { route: "/login" },
    );
    await waitFor(() => expect(screen.getByText("Welcome to Staqio")).toBeInTheDocument());
    expect(screen.getByLabelText("Confirm password")).toBeInTheDocument();
  });
});
