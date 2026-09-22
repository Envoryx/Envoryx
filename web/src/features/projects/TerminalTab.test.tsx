import { screen, waitFor, act } from "@testing-library/react";
import { TerminalTab } from "./TerminalTab";
import { authedRoutes, makeProject, mockApi, renderApp } from "@/test/utils";

class FakeSocket {
  static instances: FakeSocket[] = [];
  static OPEN = 1;
  url: string;
  readyState = 0;
  binaryType = "blob";
  sent: (string | ArrayBufferLike | ArrayBufferView)[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  onclose: ((ev: { code: number; reason: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  constructor(url: string) {
    this.url = url;
    FakeSocket.instances.push(this);
  }
  send(data: string | ArrayBufferLike | ArrayBufferView) {
    this.sent.push(data);
  }
  close() {
    this.readyState = 3;
  }
}

describe("TerminalTab", () => {
  beforeEach(() => {
    FakeSocket.instances = [];
    vi.stubGlobal("WebSocket", FakeSocket);
    vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  });
  afterEach(() => vi.unstubAllGlobals());

  it("opens a session for the selected container and refuses stopped ones", async () => {
    mockApi({ ...authedRoutes });
    renderApp(<TerminalTab project={makeProject()} />);
    await waitFor(() => expect(FakeSocket.instances).toHaveLength(1));
    const ws = FakeSocket.instances[0]!;
    expect(ws.url).toMatch(/\/services\/php\/terminal\/ws\?cols=\d+&rows=\d+$/);
    act(() => {
      ws.readyState = 1;
      ws.onopen?.();
    });
    expect(await screen.findByText("connected")).toBeInTheDocument();
    expect(screen.getByText(/runs as the project owner/)).toBeInTheDocument();
  });

  it("opens on the application container", async () => {
    mockApi({ ...authedRoutes });
    const project = makeProject();
    const web = project.services.find((s) => s.kind === "web")!;
    const node = { kind: "node", variant: "node", version: "24", image: "ghcr.io/envoryx/envoryx-node:24", enabled: true, config: { devServer: true } };
    // Web is listed first; the app container still wins the initial tab.
    project.services = [web, node];
    project.serves = "node";
    project.appService = "node";
    project.status.services = [
      { ...project.status.services.find((s) => s.kind === "web")!, running: true },
      { kind: "node", variant: "node", version: "24", image: "ghcr.io/envoryx/envoryx-node:24", containerName: "envoryx-acme-shop-node", exists: true, running: true, state: "running", ports: [], imagePrevious: false, imagePinned: false },
    ];
    renderApp(<TerminalTab project={project} />);
    await waitFor(() => expect(FakeSocket.instances).toHaveLength(1));
    expect(FakeSocket.instances[0]!.url).toMatch(/\/services\/node\/terminal\/ws\?/);
    expect(screen.getByRole("tab", { name: "Node.js 24" })).toHaveAttribute("aria-selected", "true");
  });

  it("does not connect when the container is stopped", async () => {
    mockApi({ ...authedRoutes });
    const stopped = makeProject();
    stopped.status.services = stopped.status.services.map((s) => ({ ...s, running: false, state: "exited" }));
    renderApp(<TerminalTab project={stopped} />);
    expect(await screen.findByText(/container is not running/)).toBeInTheDocument();
    expect(FakeSocket.instances).toHaveLength(0);
  });
});
