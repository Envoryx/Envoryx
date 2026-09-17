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

  it("does not connect when the container is stopped", async () => {
    mockApi({ ...authedRoutes });
    const stopped = makeProject();
    stopped.status.services = stopped.status.services.map((s) => ({ ...s, running: false, state: "exited" }));
    renderApp(<TerminalTab project={stopped} />);
    expect(await screen.findByText(/container is not running/)).toBeInTheDocument();
    expect(FakeSocket.instances).toHaveLength(0);
  });
});
