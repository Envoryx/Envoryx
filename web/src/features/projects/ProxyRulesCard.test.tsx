import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ProxyRulesCard } from "./ProxyRulesCard";
import { authedRoutes, makeProject, mockApi, renderApp } from "@/test/utils";

const id = "3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f";

describe("ProxyRulesCard", () => {
  it("saves an allowlist, a password, a redirect and a header", async () => {
    const api = mockApi({
      ...authedRoutes,
      [`PUT /projects/${id}/proxy-rules`]: () => ({ body: { project: makeProject({ id }) } }),
    });
    renderApp(<ProxyRulesCard project={makeProject({ id, hostnames: ["shop.test", "www.shop.test"] })} />);
    const user = userEvent.setup();

    expect(screen.getByText("none")).toBeInTheDocument();
    await user.type(screen.getByLabelText("Allowed addresses"), "192.168.1.0/24{enter}10.8.0.5");
    await user.click(screen.getByLabelText(/Ask for a user name and password/));
    await user.type(screen.getByLabelText("User name"), "client");
    const save = screen.getByRole("button", { name: "Save" });
    expect(save).toBeDisabled(); // no password yet
    await user.type(screen.getByLabelText("Password"), "s3cret");

    await user.click(screen.getByRole("button", { name: "Add redirect" }));
    await user.selectOptions(screen.getByLabelText("Host name"), "www.shop.test");
    await user.type(screen.getByLabelText("From"), "/*");
    await user.type(screen.getByLabelText("To"), "https://shop.test/*");
    await user.selectOptions(screen.getByLabelText("Status"), "301");
    await user.click(screen.getByRole("button", { name: "Add header" }));
    await user.type(screen.getByLabelText("Header"), "X-Powered-By");

    await user.click(save);
    await waitFor(() => expect(api.calls.some((c) => c.method === "PUT")).toBe(true));
    expect(api.calls.find((c) => c.method === "PUT")!.body).toEqual({
      allowIPs: ["192.168.1.0/24", "10.8.0.5"],
      basicAuth: { user: "client", password: "s3cret" },
      redirects: [{ host: "www.shop.test", from: "/*", to: "https://shop.test/*", status: 301 }],
      headers: [{ name: "X-Powered-By", value: "" }],
    });
    expect(await screen.findByText("Saved. The proxy applies the rules within a few seconds.")).toBeInTheDocument();
  });

  it("keeps the stored password and sets up CORS", async () => {
    const api = mockApi({
      ...authedRoutes,
      [`PUT /projects/${id}/proxy-rules`]: () => ({ body: { project: makeProject({ id }) } }),
    });
    renderApp(<ProxyRulesCard project={makeProject({ id, proxyRules: { basicAuth: { user: "client" } } })} />);
    const user = userEvent.setup();

    expect(screen.getByLabelText("Password")).toHaveValue("");
    expect(screen.getByLabelText("User name")).toHaveValue("client");
    await user.click(screen.getByLabelText(/Allow requests from other origins/));
    await user.type(screen.getByLabelText("Origins"), "https://app.test");
    await user.click(screen.getByLabelText("Allow cookies and credentials"));
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "PUT")).toBe(true));
    expect(api.calls.find((c) => c.method === "PUT")!.body).toEqual({
      basicAuth: { user: "client" },
      cors: { origins: ["https://app.test"], credentials: true },
    });
  });
});
