import { afterEach, expect, it, vi } from "vitest";

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
it("requires the explicitly selected Word or PNG media type and keeps image MIME for a verified blob", async () => {
  const word = "application/vnd.openxmlformats-officedocument.wordprocessingml.document";
  const fetcher = vi
    .fn()
    .mockResolvedValueOnce(new Response("PK", { headers: { "Content-Type": word } }))
    .mockResolvedValueOnce(
      new Response(new Uint8Array([137, 80, 78, 71]), { headers: { "Content-Type": "image/png" } }),
    )
    .mockResolvedValueOnce(new Response("<html>unexpected</html>", { headers: { "Content-Type": "text/html" } }))
    .mockResolvedValueOnce(new Response("<svg/>", { headers: { "Content-Type": "image/svg+xml" } }));
  vi.stubGlobal("fetch", fetcher);
  const { apiFetchBlob } = await import("./api");
  expect((await apiFetchBlob("/api/report/diagnostic?task=42&format=docx", {}, word)).type).toBe(
    "application/octet-stream",
  );
  expect((await apiFetchBlob("/api/report/diagnostic/image?task=42", {}, "image/png")).type).toBe("image/png");
  await expect(apiFetchBlob("/api/report/diagnostic?task=42&format=docx", {}, word)).rejects.toMatchObject({
    status: 422,
  });
  await expect(apiFetchBlob("/api/report/diagnostic/image?task=42", {}, "image/png")).rejects.toMatchObject({
    status: 422,
  });
});
