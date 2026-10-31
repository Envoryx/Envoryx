import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SshHostForm } from "./SettingsPage";
import { authedRoutes, mockApi, renderApp } from "@/test/utils";

const settings = {
  publicHost: "",
  baseDomain: "test",
  ssh: { enabled: true, port: 2222, fingerprint: "SHA256:abc" },
  lockedSettings: [] as string[],
};

describe("SshHostForm", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("saves the SSH host and port, and an empty port as 0", async () => {
    const api = mockApi({
      ...authedRoutes,
      "GET /settings": () => ({ body: settings }),
      "PATCH /settings": () => ({ body: settings }),
    });
    renderApp(
      <SshHostForm current="" envHost="" currentPort={0} envPort={0} />,
    );
    const user = userEvent.setup();
    expect(
      await screen.findByText(/Leave empty to use the address of the projects/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        "Leave empty to use the port published on the Docker host.",
      ),
    ).toBeInTheDocument();
    await user.type(screen.getByLabelText("SSH host"), "ssh.c1.example.net");
    await user.type(screen.getByLabelText("SSH port"), "2201");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(api.calls.some((c) => c.method === "PATCH")).toBe(true),
    );
    expect(api.calls.find((c) => c.method === "PATCH")!.body).toEqual({
      sshHost: "ssh.c1.example.net",
      sshPort: 2201,
    });
  });

  it("names the environment's values and locks what the plan fixes", async () => {
    mockApi({
      ...authedRoutes,
      "GET /settings": () => ({
        body: { ...settings, lockedSettings: ["sshHost", "sshPort"] },
      }),
    });
    renderApp(
      <SshHostForm
        current="ssh.c1.example.net"
        envHost="ssh.env.example.net"
        currentPort={2201}
        envPort={2022}
      />,
    );
    expect(
      await screen.findByText(
        /ENVORYX_SSH_PUBLIC_HOST \(ssh.env.example.net\)/,
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText("Leave empty to use ENVORYX_SSH_PUBLIC_PORT (2022)."),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.getByLabelText("SSH host")).toBeDisabled(),
    );
    expect(screen.getByLabelText("SSH port")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });
});
