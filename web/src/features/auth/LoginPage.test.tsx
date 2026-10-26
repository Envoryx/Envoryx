import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router-dom";
import { LoginPage, lostAccessDocs } from "./LoginPage";
import { mockApi, renderApp } from "@/test/utils";

function Home() {
  return <h1>Home</h1>;
}

describe("LoginPage", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("on a hosted instance points a lost password to the admins and the hoster, not the docs", async () => {
    mockApi({
      "GET /setup": () => ({ body: { needsSetup: false, managed: true } }),
      "GET /auth/me": () => ({ status: 401, body: { error: { code: "unauthenticated", message: "authentication required" } } }),
    });
    renderApp(<LoginPage mode="login" />, { route: "/login" });
    expect(await screen.findByText(/An admin of this instance can send you a reset link/)).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Forgot the password?" })).not.toBeInTheDocument();
  });

  it("points a lost password to the website's docs in the UI language", async () => {
    mockApi({
      "GET /setup": () => ({ body: { needsSetup: false } }),
      "GET /auth/me": () => ({ status: 401, body: { error: { code: "unauthenticated", message: "authentication required" } } }),
    });
    renderApp(<LoginPage mode="login" />, { route: "/login" });
    expect(await screen.findByRole("link", { name: "Forgot the password?" })).toHaveAttribute("href", "https://envoryx.dev/docs/operations/troubleshooting/#lost-access");
    expect(lostAccessDocs("de-DE")).toBe("https://envoryx.dev/de/docs/operations/troubleshooting/#zugang-verloren");
    expect(lostAccessDocs("fr")).toBe("https://envoryx.dev/docs/operations/troubleshooting/#lost-access");
  });

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
    expect((init?.headers as Record<string, string>)["X-Requested-With"]).toBe("Envoryx");
  });

  it("returns to the page the user came from", async () => {
    mockApi({
      "GET /setup": () => ({ body: { needsSetup: false } }),
      "GET /auth/me": () => ({ status: 401, body: { error: { code: "unauthenticated", message: "authentication required" } } }),
      "POST /auth/login": () => ({ body: { user: { id: "u1", username: "admin", role: "admin" } } }),
    });
    renderApp(
      <Routes>
        <Route path="/login" element={<LoginPage mode="login" />} />
        <Route path="/" element={<Home />} />
        <Route path="/projects" element={<h1>Projects</h1>} />
      </Routes>,
      { route: "/login", state: { from: "/projects" } },
    );
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Username"), "admin");
    await user.type(screen.getByLabelText("Password"), "supersecret123");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByRole("heading", { name: "Projects" })).toBeInTheDocument();
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
    await waitFor(() => expect(screen.getByText("Welcome to Envoryx")).toBeInTheDocument());
    expect(screen.getByLabelText("Confirm password")).toBeInTheDocument();
  });
});

describe("LoginPage second factor", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("asks for the code after the password, and takes a recovery code instead", async () => {
    const api = mockApi({
      "GET /setup": () => ({ body: { needsSetup: false } }),
      "GET /auth/me": () => ({ status: 401, body: { error: { code: "unauthenticated", message: "authentication required" } } }),
      // More specific first: the mock matches by prefix.
      "POST /auth/login/code": (_url, init) => {
        const body = JSON.parse(init.body as string);
        if (body.ticket === "t1" && (body.code === "123456" || body.recoveryCode === "abcde-fghij")) return { body: { user: { id: "u1", username: "admin", role: "admin" } } };
        return { status: 401, body: { error: { code: "invalid_code", message: "the code is wrong or was used already" } } };
      },
      "POST /auth/login": () => ({ body: { secondFactor: { ticket: "t1", methods: ["totp", "recovery"] } } }),
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
    await user.type(screen.getByLabelText("Password"), "supersecret123");
    await user.click(screen.getByRole("button", { name: "Sign in" }));

    expect(await screen.findByText("Enter the 6-digit code from your authenticator app.")).toBeInTheDocument();
    await user.type(screen.getByLabelText("Code"), "000000");
    await user.click(screen.getByRole("button", { name: "Verify" }));
    expect(await screen.findByText("The code is wrong or was used already.")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Use a recovery code" }));
    await user.type(screen.getByLabelText("Recovery code"), "abcde-fghij");
    await user.click(screen.getByRole("button", { name: "Verify" }));
    expect(await screen.findByRole("heading", { name: "Home" })).toBeInTheDocument();
    const sent = api.calls.filter((c) => c.url.endsWith("/auth/login/code")).map((c) => c.body);
    expect(sent).toEqual([
      { ticket: "t1", code: "000000" },
      { ticket: "t1", recoveryCode: "abcde-fghij" },
    ]);
  });

  it("starts over when the ticket expired", async () => {
    mockApi({
      "GET /setup": () => ({ body: { needsSetup: false } }),
      "GET /auth/me": () => ({ status: 401, body: { error: { code: "unauthenticated", message: "authentication required" } } }),
      "POST /auth/login/code": () => ({ status: 401, body: { error: { code: "second_factor_expired", message: "expired" } } }),
      "POST /auth/login": () => ({ body: { secondFactor: { ticket: "t1", methods: ["totp"] } } }),
    });
    renderApp(<LoginPage mode="login" />, { route: "/login" });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Username"), "admin");
    await user.type(screen.getByLabelText("Password"), "supersecret123");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    await user.type(await screen.findByLabelText("Code"), "123456");
    await user.click(screen.getByRole("button", { name: "Verify" }));
    expect(await screen.findByText("The sign-in took too long or failed too often; sign in again.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Sign in" })).toBeInTheDocument();
  });
});
