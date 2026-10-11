import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router-dom";
import { AppShell } from "./AppShell";
import i18n from "@/i18n";
import { authedRoutes, mockApi, renderApp } from "@/test/utils";

function shell() {
  return (
    <Routes>
      <Route element={<AppShell />}>
        <Route path="/" element={<h1>Home</h1>} />
      </Route>
    </Routes>
  );
}

describe("AppShell", () => {
  afterEach(async () => {
    vi.unstubAllGlobals();
    localStorage.clear();
    await i18n.changeLanguage("en");
  });

  it("closes the mobile menu on Escape", async () => {
    mockApi({ ...authedRoutes, "GET /operations": () => ({ body: { operations: [] } }) });
    renderApp(shell());
    const user = userEvent.setup();
    // The desktop sidebar is always in the DOM (CSS hides it on a phone); the drawer adds a copy.
    await user.click(await screen.findByRole("button", { name: "Open menu" }));
    expect(screen.getAllByRole("navigation", { name: "Main navigation" })).toHaveLength(2);
    await user.keyboard("{Escape}");
    expect(screen.getAllByRole("navigation", { name: "Main navigation" })).toHaveLength(1);
  });

  it("words the theme button and the logo link in the chosen language", async () => {
    mockApi({ ...authedRoutes, "GET /operations": () => ({ body: { operations: [] } }) });
    localStorage.setItem("envoryx.theme", "light");
    await i18n.changeLanguage("de");
    renderApp(shell());
    expect(await screen.findByRole("button", { name: "Design: hell" })).toBeInTheDocument();
    expect(screen.getAllByTitle("Envoryx - Die räumliche Fabrik").length).toBeGreaterThan(0);
  });
});
