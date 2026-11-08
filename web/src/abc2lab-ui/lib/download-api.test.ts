import { afterEach, expect, it, vi } from "vitest";

vi.mock("./mock", () => ({ MOCK: false }));
afterEach(() => {
  vi.unstubAllGlobals();
  vi.resetModules();
  localStorage.clear();
});
it("uses the existing bearer session without URL credentials and rejects redirects", async () => {
  localStorage.setItem("abc2lab.devToken", "synthetic-token");
  const fetcher = vi
    .fn()
    .mockResolvedValue(new Response("<html>safe</html>", { headers: { "Content-Type": "text/html; charset=utf-8" } }));
  vi.stubGlobal("fetch", fetcher);
  const { apiFetchBlob } = await import("./api");
  const blob = await apiFetchBlob("/api/report/diagnostic?task=42&audience=shared&download=1");
  expect(blob.type).toBe("application/octet-stream");
  const [url, init] = fetcher.mock.calls[0];
  expect(url).not.toContain("synthetic-token");
  expect(init.headers.get("Authorization")).toBe("Bearer synthetic-token");
  expect(init.redirect).toBe("error");
  expect(init.cache).toBe("no-store");
});
it("clears expired auth, retries once and never downloads an error", async () => {
  localStorage.setItem("abc2lab.devToken", "expired");
  const fetcher = vi.fn().mockResolvedValue(new Response("{}", { status: 401 }));
  vi.stubGlobal("fetch", fetcher);
  const { apiFetchBlob } = await import("./api");
  await expect(apiFetchBlob("/api/report/diagnostic?task=42")).rejects.toMatchObject({ status: 401 });
  expect(fetcher).toHaveBeenCalledTimes(2);
  expect(localStorage.getItem("abc2lab.devToken")).toBeNull();
});
it("rejects a non-HTML success response", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(new Response("{}", { headers: { "Content-Type": "application/json" } })),
  );
  const { apiFetchBlob } = await import("./api");
  await expect(apiFetchBlob("/api/report/diagnostic?task=42")).rejects.toMatchObject({ status: 422 });
});
