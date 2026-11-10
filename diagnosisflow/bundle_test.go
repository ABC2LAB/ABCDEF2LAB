package diagnosisflow

import (
	"context"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
)

func refStrings(refs []bundleRef) []string {
	out := []string{}
	for _, r := range refs {
		out = append(out, r.method+" "+r.path+" "+r.source)
	}
	return out
}

func TestBundleRefsReadRequestShapes(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want []string
	}{
		"direct literal":        {`c.delete("/rest/item")`, []string{"DELETE /rest/item script_call"}},
		"relative literal":      {`c.post("./rest/verify",x)`, []string{"POST ./rest/verify script_call"}},
		"host prefix":           {`this.http.post(this.server+"/rest/login",e)`, []string{"POST /rest/login script_call"}},
		"other expression":      {`http.put(env.api+"/v1/item",e)`, []string{"PUT /v1/item script_call"}},
		"base and suffix":       {`this.host=this.server+"/svc/items"}all(){return this.http.get(this.host+"/all")}`, []string{"GET /svc/items/all script_call"}},
		"bare base":             {`this.host=this.server+"/svc/items"}all(){return this.http.get(this.host)}`, []string{"GET /svc/items script_call"}},
		"bare base with body":   {`this.host="/svc/items"}add(e){return this.http.post(this.host,e)}`, []string{"POST /svc/items script_call"}},
		"bare unknown base":     {`this.http.get(this.host)`, []string{}},
		"template base":         {"this.host=`${env}/svc/t`}x(){return this.http.get(this.host+\"/a\")}", []string{"GET /svc/t/a script_call"}},
		"template host":         {"this.http.get(`${this.server}/rest/search`,e)", []string{"GET /rest/search script_call"}},
		"template dynamic":      {"this.host=\"/svc/items\"}x(e){return this.http.get(`${this.host}/${e}`)}", []string{"GET /svc/items script_prefix"}},
		"template dynamic deep": {"this.http.get(`${this.server}/rest/basket/${e}`)", []string{"GET /rest/basket script_prefix"}},
		"concat dynamic":        {`this.host="/svc/items"}x(e){return this.http.get(this.host+"/"+e)}`, []string{"GET /svc/items script_prefix"}},
		"concat dynamic deep":   {`this.http.get(this.server+"/rest/basket/" + e)`, []string{"GET /rest/basket script_prefix"}},
		"concat only dynamic":   {`this.http.get(this.server+"/"+e)`, []string{}},
		"fetch default":         {`fetch("/data/list")`, []string{"GET /data/list script_call"}},
		"fetch with method":     {`fetch("/data/add",{headers:h,method:"post"})`, []string{"POST /data/add script_call"}},
		"xhr":                   {`x.open("PATCH","/data/one")`, []string{"PATCH /data/one script_call"}},
		"url field":             {`ajax({url:"/data/field"})`, []string{"UNKNOWN /data/field static_literal"}},
		"api literal":           {`var a="/api/thing"`, []string{"UNKNOWN /api/thing static_literal"}},
		"known method wins":     {`var a="/api/thing";c.get("/api/thing")`, []string{"GET /api/thing script_call"}},
		"translation key":       {`t.get("ADDRESS_ADDED");t.get("a.b")`, []string{}},
		"map delete":            {`m.delete("key");m.get(k)`, []string{}},
		"absolute other host":   {`c.get("https://other.test/x")`, []string{}},
		"duplicate":             {`c.get("/a");c.get("/a")`, []string{"GET /a script_call"}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := refStrings(bundleRefs([]byte(tc.body))); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestBundleBaseUsesNearestEarlierAssignment(t *testing.T) {
	body := `this.host="/first"}a(){return this.http.get(this.host+"/x")}this.host="/second"}b(){return this.http.get(this.host+"/y")}`
	want := []string{"GET /first/x script_call", "GET /second/y script_call"}
	if got := refStrings(bundleRefs([]byte(body))); !reflect.DeepEqual(got, want) {
		t.Fatalf("%q", got)
	}
	named := `this.a="/first";this.b="/second";c.get(this.a+"/x")`
	if got := refStrings(bundleRefs([]byte(named))); !reflect.DeepEqual(got, []string{"GET /first/x script_call"}) {
		t.Fatalf("other base used %q", got)
	}
	mixed := "this.host=`/template`;this.host=\"/literal\";c.get(this.host+\"/x\")"
	if got := refStrings(bundleRefs([]byte(mixed))); !reflect.DeepEqual(got, []string{"GET /literal/x script_call"}) {
		t.Fatalf("earlier template used %q", got)
	}
	later := `a(){return this.http.get(this.host+"/x")}this.host="/late"`
	if got := refStrings(bundleRefs([]byte(later))); !reflect.DeepEqual(got, []string{"GET /x script_call"}) {
		t.Fatalf("later assignment used %q", got)
	}
}

func TestBundleChunks(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want []string
	}{
		"relative":     {`import("./chunk-a.js")`, []string{"./chunk-a.js"}},
		"directory":    {`["assets/b.mjs"]`, []string{"assets/b.mjs"}},
		"absolute":     {`"/static/c.js"`, []string{"/static/c.js"}},
		"bare module":  {`require("moment.js")`, nil},
		"node modules": {`"node_modules/x/y.js"`, nil},
		"map":          {`r.u=e=>({1:"abc",2:"def"})[e]+".js"`, []string{"abc.js", "def.js"}},
		"id and hash":  {`r.u=e=>e+"."+{7:"abc"}[e]+".js"`, []string{"7.abc.js"}},
		"prefix":       {`r.u=e=>"chunk-"+{7:"abc"}[e]+".js"`, []string{"chunk-abc.js"}},
		"public path":  {`r.p="/static/js/",r.u=e=>({7:"abc"})[e]+".js"`, []string{"/static/js/abc.js"}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := bundleChunks([]byte(tc.body)); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestBundleChunksAreCapped(t *testing.T) {
	var b strings.Builder
	for i := 0; i < maxBundleChunks+20; i++ {
		b.WriteString(`"/c` + strconv.Itoa(i) + `.js",`)
	}
	if got := bundleChunks([]byte(b.String())); len(got) != maxBundleChunks {
		t.Fatal(len(got))
	}
}

func TestBundleChunkIsScriptKind(t *testing.T) {
	refs := bundleRefs([]byte(`"/static/c.js"`))
	if len(refs) != 1 || refs[0] != (bundleRef{"GET", "/static/c.js", "script", "bundle_chunk"}) {
		t.Fatalf("%+v", refs)
	}
}

func discovered(c *Controller) []string {
	out := []string{}
	for _, e := range c.record.Snapshot.Endpoints {
		out = append(out, e.Method+" "+e.Path+" "+e.Kind)
	}
	return out
}

func TestHTMLPreloadScriptsAreDiscovered(t *testing.T) {
	c, _ := flowFixture(t, Manual)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.discoverBodyLocked([]byte(`<link rel="modulepreload" href="/read/a.js"><link rel="PRELOAD" as="script" href="/read/b.js"><link rel="preload" as="style" href="/read/c.css"><link rel="stylesheet" href="/read/d.css">`), "text/html", "/read")
	got := discovered(c)
	if !slices.Contains(got, "GET /read/a.js script") || !slices.Contains(got, "GET /read/b.js script") || slices.Contains(got, "GET /read/c.css script") || slices.Contains(got, "GET /read/d.css script") {
		t.Fatal(got)
	}
}

func TestDiscoveryResolvesBundleCallsAgainstOrigin(t *testing.T) {
	c, _ := flowFixture(t, Manual)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.discoverBodyLocked([]byte(`this.host=s+"/read/items"}a(){return this.http.post(this.host+"/add",e)}`), "application/javascript", "/read/app.js")
	if got := discovered(c); !slices.Contains(got, "POST /read/items/add api") {
		t.Fatal(got)
	}
}

func TestCrawlDoesNotRefetchAFailedPage(t *testing.T) {
	c, _ := flowFixture(t, Manual)
	c.binding.Limits.MaxPages = 3
	var order []string
	handled, err := c.Crawl(t.Context(), func(ctx context.Context, target string) (e2runner.Result, error) {
		path := strings.TrimPrefix(target, "https://owned.test")
		order = append(order, path)
		trusted := e2runner.TrustedResult{Version: e2proxy.Version, Final: true, ProcessDrained: true, ProxyDrained: true, Call: e2proxy.CallResult{Final: true, AuditComplete: true, Counts: e2proxy.Counts{ForwardAttempted: 1}}}
		if path != "/read" {
			trusted.Call.Counts.ForwardAttempted = 0
		}
		return e2runner.Result{ToolOutput: "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\n\r\n<a href=\"/read/bad\">b</a>", Trusted: trusted}, nil
	})
	if !handled || err != nil {
		t.Fatal(handled, err)
	}
	if want := []string{"/read", "/read/bad"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("got=%q want=%q", order, want)
	}
}

func TestCrawlRankOrdersScriptsBeforePagesAndVendorsLast(t *testing.T) {
	for _, tc := range []struct {
		e    Endpoint
		want int
	}{
		{Endpoint{Source: "start", Kind: "page", Path: "/"}, 0},
		{Endpoint{Kind: "script", Path: "/main.js"}, 1},
		{Endpoint{Kind: "script", Path: "/assets/index-abc.js"}, 1},
		{Endpoint{Kind: "page", Path: "/about"}, 2},
		{Endpoint{Kind: "script", Path: "/vendor.js"}, 3},
		{Endpoint{Kind: "script", Path: "/js/chunk-vendors.abc.js"}, 3},
		{Endpoint{Kind: "script", Path: "/polyfills.js"}, 3},
		{Endpoint{Kind: "page", Path: "/vendor.js"}, 2},
	} {
		if got := crawlRank(tc.e); got != tc.want {
			t.Errorf("%+v got=%d want=%d", tc.e, got, tc.want)
		}
	}
}

func TestCrawlFetchesBundleBeforeOtherPagesWithinPageLimit(t *testing.T) {
	c, _ := flowFixture(t, Manual)
	c.binding.Limits.MaxPages = 3
	var order []string
	handled, err := c.Crawl(t.Context(), func(ctx context.Context, target string) (e2runner.Result, error) {
		order = append(order, strings.TrimPrefix(target, "https://owned.test"))
		view := e2proxy.RequestView{Method: "GET", URL: target}
		if c.Before(ctx, view) != nil {
			t.Fatal("outside scope", target)
		}
		receipt := e2proxy.Receipt{AttemptID: statusText(len(order)), CallRef: "owned-call", Decision: "allowed", ForwardAttempted: true, RequestWritten: true, ResponseReceived: true, ResponseComplete: true, StatusCode: 200}
		if c.After(ctx, view, receipt) != nil {
			t.Fatal("persist")
		}
		body := `<a href="/read/page">p</a><script src="/read/vendor.js"></script><script src="/read/app.js"></script>`
		return e2runner.Result{ToolOutput: "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\n\r\n" + body, Trusted: e2runner.TrustedResult{Version: e2proxy.Version, Final: true, ProcessDrained: true, ProxyDrained: true, Call: e2proxy.CallResult{Final: true, AuditComplete: true, Counts: e2proxy.Counts{ForwardAttempted: 1}}}}, nil
	})
	if !handled || err != nil {
		t.Fatal(handled, err)
	}
	if want := []string{"/read", "/read/app.js", "/read/page"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("got=%q want=%q", order, want)
	}
}
