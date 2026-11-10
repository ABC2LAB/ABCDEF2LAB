import { describe, expect, it } from "vitest";

import { isInternalUrl } from "./net";

describe("internal address hints", () => {
  it.each([
    ["http://localhost:8787", true],
    ["http://10.1.2.3", true],
    ["http://172.16.0.1", true],
    ["http://172.32.0.1", false],
    ["http://192.168.1.2", true],
    ["http://[::1]:11434/v1", true],
    ["http://[fd00::1]", true],
    ["http://[fe80::1]", true],
    ["http://[fe90::1]", true],
    ["http://[febf::1]", true],
    ["http://[fec0::1]", false],
    ["http://[2001:4860:4860::8888]", false],
    ["https://localhost.attacker.example", false],
    ["https://remote.example/v1?name=ollama", false],
    ["invalid URL", null],
  ] as const)("classifies %s as %s", (url, expected) => {
    expect(isInternalUrl(url)).toBe(expected);
  });
});
