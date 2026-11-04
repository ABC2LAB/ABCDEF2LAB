import { describe, expect, it } from "vitest";

import { buildAssetGraph } from "./asset-graph";
import type { EngineCoverageNode, EngineFinding } from "./engine";

const ep = (id: number, url: string, tested = true, in_scope = true): EngineCoverageNode => ({
  key: `a:${id}`,
  kind: "endpoint",
  label: url,
  url,
  tested,
  in_scope,
  asset_id: id,
});
const finding = (id: string, assets: EngineFinding["assets"]): EngineFinding => ({
  id,
  vulnclass: "IDOR",
  name: id,
  severity: "high",
  status: "pending",
  summary: "",
  assets,
  ts: "2026-11-04T00:00:00Z",
});

describe("buildAssetGraph", () => {
  const nodes: EngineCoverageNode[] = [
    {
      key: "a:1",
      kind: "service",
      label: "https://shop.test",
      url: "https://shop.test",
      tested: true,
      in_scope: true,
      asset_id: 1,
    },
    ep(10, "https://shop.test/"),
    ep(11, "https://shop.test/login"),
    ep(12, "https://shop.test/api/orders/7"),
    ep(13, "https://shop.test/api/users?id=3&sort=asc", false),
    ep(14, "https://shop.test/admin/stats"),
    ep(15, "https://shop.test/products/1"),
    ep(16, "https://shop.test/products/2", false),
    ep(20, "http://cdn.test:8080/app.js"),
  ];

  it("groups endpoints into domain → service → path group", () => {
    const g = buildAssetGraph({ nodes, edges: [] }, []);
    expect(g.blocks.map((b) => b.service)).toEqual(["shop.test:443", "cdn.test:8080"]);
    const shop = g.blocks[0];
    expect(shop.domain).toBe("shop.test");
    // 한 개뿐인 /login 은 페이지로, 두 개 모인 /products 와 API·관리자 접두사는 따로 묶는다
    expect(shop.groups.map((x) => [x.key, x.name, x.eps.length])).toEqual([
      ["/", "페이지", 2],
      ["/admin", "관리자", 1],
      ["/api", "API", 2],
      ["/products", "", 2],
    ]);
    // 쿼리는 키만 남긴다
    expect(shop.groups[2].eps.map((e) => e.path)).toContain("/api/users?id=&sort=");
  });

  it("shows path templates as written, not percent-encoded", () => {
    const g = buildAssetGraph({ nodes: [ep(40, "https://shop.test/api/orders/{id}")], edges: [] }, [
      finding("tpl", [{ id: "z", type: "endpoint", label: "GET /api/orders/{id}" }]),
    ]);
    const only = g.blocks[0].groups[0].eps[0];
    expect(only.path).toBe("/api/orders/{id}");
    expect(only.findings.map((f) => f.id)).toEqual(["tpl"]);
  });

  it("counts tested against in-scope endpoints only", () => {
    const g = buildAssetGraph(
      { nodes: [...nodes, ep(30, "https://shop.test/admin/delete", false, false)], edges: [] },
      [],
    );
    expect(g.stats).toEqual({ inScope: 8, tested: 6, out: 1 });
  });

  it("attaches findings by asset id, then by path, then to the service", () => {
    const g = buildAssetGraph({ nodes, edges: [] }, [
      finding("by-id", [{ id: "12", type: "endpoint", label: "GET /something/else" }]),
      finding("by-path", [{ id: "x", type: "endpoint", label: "GET /admin/stats" }]),
      finding("site", [{ id: "1", type: "service", label: "https://shop.test" }]),
      finding("loose", [{ id: "y", type: "endpoint", label: "GET /nowhere" }]),
      finding("none", []),
    ]);
    const shop = g.blocks[0];
    const at = (path: string) =>
      shop.groups
        .flatMap((x) => x.eps)
        .find((e) => e.path === path)
        ?.findings.map((f) => f.id);
    expect(at("/api/orders/7")).toEqual(["by-id"]);
    expect(at("/admin/stats")).toEqual(["by-path"]);
    expect(shop.siteFindings.map((f) => f.id)).toEqual(["site"]);
    expect(g.loose.map((f) => f.id)).toEqual(["loose", "none"]);
  });

  it("keeps an empty service as its own block", () => {
    const g = buildAssetGraph(
      {
        nodes: [
          {
            key: "a:2",
            kind: "service",
            label: "",
            domain: "db.test",
            port: 5432,
            tested: false,
            in_scope: true,
            asset_id: 2,
          },
        ],
        edges: [],
      },
      [],
    );
    expect(g.blocks).toEqual([{ service: "db.test:5432", domain: "db.test", siteFindings: [], groups: [] }]);
  });
});
