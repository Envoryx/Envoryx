import { screen, waitFor, act } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ActionsTab } from "./ActionsTab";
import { authedRoutes, makeProject, mockApi, renderApp } from "@/test/utils";

class FakeSocket {
  static instances: FakeSocket[] = [];
  static OPEN = 1;
  url: string;
  readyState = 1;
  binaryType = "blob";
  sent: string[] = [];
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  onclose: ((ev: { code: number; reason: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  constructor(url: string) {
    this.url = url;
    FakeSocket.instances.push(this);
  }
  send(d: string) {
    this.sent.push(d);
  }
  close() {
    this.readyState = 3;
  }
}

const actions = [
  { id: "composer:install", group: "Composer", label: "composer install", description: "Install deps", service: "php", cmd: ["composer", "install"], requires: ["composer.json"], destructive: false, available: true },
  { id: "artisan:migrate-fresh", group: "Artisan", label: "artisan migrate:fresh --seed", description: "Drop and seed", service: "php", cmd: ["php", "artisan", "migrate:fresh"], requires: ["artisan"], destructive: true, available: false, reason: "artisan not found in project" },
];

describe("ActionsTab", () => {
  beforeEach(() => {
    FakeSocket.instances = [];
    vi.stubGlobal("WebSocket", FakeSocket);
    vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  });
  afterEach(() => vi.unstubAllGlobals());

  it("lists actions with availability and streams a run", async () => {
    mockApi({ ...authedRoutes, "GET /projects/3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f/actions": () => ({ body: { actions } }) });
    renderApp(<ActionsTab project={makeProject()} />);
    const user = userEvent.setup();
    const install = await screen.findByRole("button", { name: /composer install/ });
    const fresh = screen.getByRole("button", { name: /migrate:fresh/ });
    expect(fresh).toBeDisabled();
    expect(screen.getByText("artisan not found in project")).toBeInTheDocument();

    await user.click(install);
    await waitFor(() => expect(FakeSocket.instances).toHaveLength(1));
    const ws = FakeSocket.instances[0]!;
    expect(ws.url).toContain("/actions/composer%3Ainstall/ws?cols=");
    expect(screen.getByText("running")).toBeInTheDocument();
    expect(install).toBeDisabled();

    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(ws.sent).toContain('{"type":"cancel"}');

    act(() => ws.onmessage?.({ data: JSON.stringify({ type: "exit", code: 0 }) }));
    expect(await screen.findByText("finished")).toBeInTheDocument();
    expect(install).not.toBeDisabled();
  });
});
