import { describe, expect, it } from "vitest";
import { gitUrlError } from "./gitUrl";

describe("gitUrlError", () => {
  it("passes what the server accepts", () => {
    for (const u of ["https://github.com/you/project.git", "http://git.local/a", "ssh://git@host:2222/a.git", "git@github.com:you/project.git", "  https://x.io/y  ", ""]) {
      expect(gitUrlError(u), u).toBeUndefined();
    }
  });
  it("wants a scheme", () => {
    expect(gitUrlError("github.com/you/project")).toBe("repository URL must use https://, ssh:// or git@host:path");
    expect(gitUrlError("file:///srv/repo")).toBe("repository URL must use https://, ssh:// or git@host:path");
    expect(gitUrlError("/srv/repo")).toBe("repository URL must use https://, ssh:// or git@host:path");
  });
  it("waits while a scheme or path is still being typed", () => {
    for (const u of ["h", "HTTPS:/", "ssh:", "git@gith", "https://github.com"]) expect(gitUrlError(u, true), u).toBeUndefined();
    expect(gitUrlError("github.com", true)).toBe("repository URL must use https://, ssh:// or git@host:path");
    expect(gitUrlError("https://github.com")).toBe("repository URL must contain a host and a path");
  });
  it("refuses credentials and odd characters", () => {
    expect(gitUrlError("https://me:secret@github.com/a")).toBe("do not put credentials into the URL; use the access token field");
    expect(gitUrlError("https://github.com/a b")).toBe("repository URL contains unsupported characters");
    expect(gitUrlError("-uhttps://x/y")).toBe("repository URL contains unsupported characters");
  });
});
