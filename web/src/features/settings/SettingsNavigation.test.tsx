import { screen, within } from "@testing-library/react";
import { SettingsPage } from "./SettingsPage";
import { authedRoutes, mockApi, renderApp } from "@/test/utils";

describe("Settings navigation", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("groups the admin's sections and keeps their own account among them", async () => {
    mockApi({
      ...authedRoutes,
      "GET /system/diagnostics": () => ({ body: { checks: [], summary: { ok: 1, info: 0, warning: 0, error: 0 }, at: "2026-09-29T12:00:00Z" } }),
    });
    // An old link to the Account tab, which admins never had, now opens their profile.
    renderApp(<SettingsPage />, { route: "/settings?tab=account" });
    const nav = await screen.findByRole("navigation", { name: "Settings sections" });
    for (const group of ["Instance", "Access & security", "Operations", "Extensions", "My account"]) expect(within(nav).getByText(group)).toBeInTheDocument();
    expect(within(nav).getByRole("link", { name: "Profile" })).toHaveAttribute("aria-current", "page");
    expect(within(nav).getByRole("link", { name: "Retention" })).toHaveAttribute("href", "/settings?tab=retention");
    expect(within(nav).getByRole("link", { name: /Diagnostics/ })).toHaveAttribute("href", "/settings");
    expect(await screen.findByRole("heading", { name: /password/i })).toBeInTheDocument();
  });
});
