import { describe, expect, it } from "vitest";

import { REDACTED, redactInput } from "./redact";

describe("redactInput", () => {
  it("hides secret-looking keys but keeps the request shape", () => {
    const out = redactInput({
      method: "PUT",
      url: "https://shop.test/api/users/2",
      headers: { Cookie: "session=abc", Authorization: "Bearer xyz", "Content-Type": "application/json" },
      body: { role: "admin", password: "hunter2" },
    });
    expect(out).toContain('"url": "https://shop.test/api/users/2"');
    expect(out).toContain('"role": "admin"');
    expect(out).toContain('"Content-Type": "application/json"');
    for (const secret of ["abc", "xyz", "hunter2"]) expect(out).not.toContain(secret);
    expect(out.match(new RegExp(REDACTED.replace(/[[\]]/g, "\\$&"), "g"))).toHaveLength(3);
  });

  it("hides headers, bearer tokens and secret params inside command strings", () => {
    const out = redactInput({
      command: "curl -H 'Authorization: Bearer t0k' -b 'sid=1' 'https://x.test/a?token=zz&id=3'",
    });
    for (const secret of ["t0k", "sid=1", "zz"]) expect(out).not.toContain(secret);
    expect(out).toContain("id=3");
  });

  it("accepts a JSON string or plain text and returns empty for nothing", () => {
    expect(redactInput('{"a":1}')).toBe('{\n  "a": 1\n}');
    expect(redactInput("Cookie: s=1\nX: y")).toBe(`Cookie: ${REDACTED}\nX: y`);
    expect(redactInput(undefined)).toBe("");
  });
});
