import { screen, waitFor } from "@testing-library/react";
import { Route, Routes } from "react-router-dom";
import { GatePage } from "./GatePage";
import { mockApi, renderApp } from "@/test/utils";

const routes = (
  <Routes>
    <Route path="/gate" element={<GatePage />} />
  </Routes>
);
const to = "https://shop-phpmyadmin.test/index.php";

describe("GatePage", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("passes the browser on with the address the server hands out", async () => {
    const replace = vi.fn();
    vi.stubGlobal("location", { ...window.location, replace });
    const { calls } = mockApi({
      "POST /auth/gate": () => ({ body: { url: "https://shop-phpmyadmin.test/.envoryx/gate?code=abc", name: "Shop (phpMyAdmin)" } }),
    });
    renderApp(routes, { route: `/gate?to=${encodeURIComponent(to)}` });
    await waitFor(() => expect(replace).toHaveBeenCalledWith("https://shop-phpmyadmin.test/.envoryx/gate?code=abc"));
    expect(calls.filter((c) => c.url.endsWith("/auth/gate")).map((c) => c.body)).toEqual([{ to }]);
  });

  it("says why a role doesn't let the user in", async () => {
    mockApi({
      "POST /auth/gate": () => ({ status: 403, body: { error: { code: "forbidden", message: "your role does not allow this (it needs operate access)" } } }),
    });
    renderApp(routes, { route: `/gate?to=${encodeURIComponent(to)}` });
    expect(await screen.findByText("Your role in this project doesn't let you open this page.")).toBeInTheDocument();
    expect(screen.getByText(to)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to projects" })).toHaveAttribute("href", "/projects");
  });
});
