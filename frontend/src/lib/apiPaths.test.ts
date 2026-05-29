import { describe, expect, it } from "vitest";

import { adminLeasePath } from "@/lib/apiPaths";

describe("API_PATHS contract alignment", () => {
  it("encodes lease identities as base64url path segments", () => {
    const name = "relay-1";
    const address = "0x00000000000000000000000000000000000000A1";
    const expectedName = Buffer.from(name)
      .toString("base64")
      .replace(/\+/g, "-")
      .replace(/\//g, "_")
      .replace(/=+$/, "");
    const expectedAddress = Buffer.from(address)
      .toString("base64")
      .replace(/\+/g, "-")
      .replace(/\//g, "_")
      .replace(/=+$/, "");
    expect(expectedAddress).not.toContain("=");
    expect(adminLeasePath(name, address, "approve")).toBe(
      `/admin/leases/${encodeURIComponent(expectedName)}/${encodeURIComponent(expectedAddress)}/approve`
    );
  });
});
