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
		"host prefix":           {`this.http.post(this.server+"/rest/login",e)`, []string{"POST /rest/login script_guess"}},
		"other expression":      {`http.put(env.api+"/v1/item",e)`, []string{"PUT /v1/item script_guess"}},
		"local variable":        {`const u=this.host+"/"+id;this.http.get(u+"/reviews")`, []string{"UNKNOWN /reviews static_literal"}},
		"local template":        {"c.get(`${u}/rest/x`)", []string{"UNKNOWN /rest/x static_literal"}},
		"base and suffix":       {`this.host=this.server+"/svc/items"}all(){return this.http.get(this.host+"/all")}`, []string{"GET /svc/items/all script_call"}},
		"bare base":             {`this.host=this.server+"/svc/items"}all(){return this.http.get(this.host)}`, []string{"GET /svc/items script_call"}},
		"bare base with body":   {`this.host="/svc/items"}add(e){return this.http.post(this.host,e)}`, []string{"POST /svc/items script_call"}},
		"bare unknown base":     {`this.http.get(this.host)`, []string{}},
		"template base":         {"this.host=`${env}/svc/t`}x(){return this.http.get(this.host+\"/a\")}", []string{"GET /svc/t/a script_call"}},
		"template host":         {"this.http.get(`${this.server}/rest/search`,e)", []string{"GET /rest/search script_guess"}},
		"template dynamic":      {"this.host=\"/svc/items\"}x(e){return this.http.get(`${this.host}/${e}`)}", []string{"GET /svc/items script_prefix"}},
		"template dynamic deep": {"this.http.get(`${this.server}/rest/basket/${e}`)", []string{"GET /rest/basket script_prefix"}},
		"concat dynamic":        {`this.host="/svc/items"}x(e){return this.http.get(this.host+"/"+e)}`, []string{"GET /svc/items script_prefix"}},
		"concat dynamic deep":   {`this.http.get(this.server+"/rest/basket/" + e)`, []string{"GET /rest/basket script_prefix"}},
		"concat only dynamic":   {`this.http.get(this.server+"/"+e)`, []string{}},
		"fetch default":         {`fetch("/data/list")`, []string{"GET /data/list script_call"}},
		"fetch with method":     {`fetch("/data/add",{headers:h,method:"post"})`, []string{"POST /data/add script_call"}},
		"fetch nested options":  {`fetch("/data/add",{headers:{a:1},method:"POST"})`, []string{"UNKNOWN /data/add script_call"}},
		"fetch variable init":   {`fetch("/data/add",init)`, []string{"UNKNOWN /data/add script_call"}},
		"fetch base":            {`this.b="/api";fetch(this.b+"/z")`, []string{"GET /api/z script_call"}},
		"fetch dynamic":         {`fetch("/data/one/"+id)`, []string{"UNKNOWN /data/one script_prefix"}},
		"fetch local":           {`fetch(u+"/z")`, []string{"UNKNOWN /z static_literal"}},
		"xhr":                   {`x.open("PATCH","/data/one")`, []string{"PATCH /data/one script_call"}},
		"xhr lower method":      {`x.open("post","/data/two")`, []string{"POST /data/two script_call"}},
		"xhr relative":          {`x.open("GET","data/three")`, []string{"GET data/three script_call"}},
		"xhr dynamic":           {`x.open("GET","/data/four/"+id)`, []string{"GET /data/four script_prefix"}},
		"url field":             {`ajax({url:"/data/field"})`, []string{"UNKNOWN /data/field static_literal"}},
		"api literal":           {`var a="/api/thing"`, []string{"UNKNOWN /api/thing static_literal"}},
		"known method wins":     {`var a="/api/thing";c.get("/api/thing")`, []string{"GET /api/thing script_call"}},
		"translation key":       {`t.get("ADDRESS_ADDED");t.get("a.b")`, []string{}},
		"map delete":            {`m.delete("key");m.get(k)`, []string{}},
		"bare http client":      {`this.http.get("old")`, []string{"GET old script_call"}},
		"bare client file":      {`this.httpClient.get("data.json")`, []string{"GET data.json script_call"}},
		"bare axios":            {`axios.post("save",x)`, []string{"POST save script_call"}},
		"bare jquery":           {`;$.get("data")`, []string{"GET data script_call"}},
		"bare dynamic":          {`this.http.get("old"+id)`, []string{"GET old script_prefix"}},
		"bare jQuery name":      {`jQuery.get("e")`, []string{"GET e script_call"}},
		"bare private http":     {`this._http.get("a")`, []string{"GET a script_call"}},
		"bare angular http":     {`$http.get("b")`, []string{"GET b script_call"}},
		"bare minified this":    {`e.http.get("c")`, []string{"GET c script_call"}},
		"http params":           {`httpParams.get("page")`, []string{}},
		"api config":            {`this.apiConfig.get("timeout")`, []string{}},
		"request cache":         {`requestCache.get("token")`, []string{}},
		"rapid word":            {`rapid.get("x")`, []string{}},
		"bare opaque receiver":  {`c.get("old")`, []string{}},
		"translation service":   {`this.translate.get("ADDRESS_ADDED")`, []string{}},
		"dollar in name":        {`a$.get("KEY")`, []string{}},
		"relative without dot":  {`c.get("rest/x")`, []string{"GET rest/x script_call"}},
		"same origin absolute":  {`c.get("https://owned.test/rest/x")`, []string{"GET https://owned.test/rest/x script_call"}},
		"fetch relative":        {`fetch("rest/y")`, []string{"GET rest/y script_call"}},
		"empty template":        {"c.get(`${a}${b}`)", []string{}},
		"duplicate":             {`c.get("/a");c.get("/a")`, []string{"GET /a script_call"}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := refStrings(bundleRefs([]byte(tc.body), maxBundleChunks)); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestBundleBaseUsesNearestEarlierAssignment(t *testing.T) {
	body := `this.host="/first"}a(){return this.http.get(this.host+"/x")}this.host="/second"}b(){return this.http.get(this.host+"/y")}`
	want := []string{"GET /first/x script_call", "GET /second/y script_call"}
	if got := refStrings(bundleRefs([]byte(body), maxBundleChunks)); !reflect.DeepEqual(got, want) {
		t.Fatalf("%q", got)
	}
	named := `this.a="/first";this.b="/second";c.get(this.a+"/x")`
	if got := refStrings(bundleRefs([]byte(named), maxBundleChunks)); !reflect.DeepEqual(got, []string{"GET /first/x script_call"}) {
		t.Fatalf("other base used %q", got)
	}
	mixed := "this.host=`/template`;this.host=\"/literal\";c.get(this.host+\"/x\")"
	if got := refStrings(bundleRefs([]byte(mixed), maxBundleChunks)); !reflect.DeepEqual(got, []string{"GET /literal/x script_call"}) {
		t.Fatalf("earlier template used %q", got)
	}
	later := `a(){return this.http.get(this.host+"/x")}this.host="/late"`
	if got := refStrings(bundleRefs([]byte(later), maxBundleChunks)); !reflect.DeepEqual(got, []string{"GET /x script_guess"}) {
		t.Fatalf("later assignment used %q", got)
	}
}

func TestBundleBaseWithDynamicTailIsNotUsed(t *testing.T) {
	got := refStrings(bundleRefs([]byte(`this.host=s+"/api"+t;this.http.delete(this.host+"/1");this.http.get(this.host)`), maxBundleChunks))
	if want := []string{"UNKNOWN /1 static_literal"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%q want=%q", got, want)
	}
	// 뒤에 온전한 대입이 있으면 그 값을 쓴다.
	got = refStrings(bundleRefs([]byte(`this.host=s+"/api"+t;this.host="/ok";c.get(this.host+"/1")`), maxBundleChunks))
	if want := []string{"GET /ok/1 script_call"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestBundleBaseCapStopsResolvingAfterTruncation(t *testing.T) {
	assignments := func(n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString(`this.r` + strconv.Itoa(i) + `="/route";`)
		}
		return b.String()
	}
	early := `this.host="/first";c.get(this.host+"/a");`
	late := `this.host="/later";c.get(this.host+"/x");this.http.get(this.host)`
	// 첫 대입과 합쳐 한도를 채우므로 late의 대입은 수집되지 않는다.
	truncated := refStrings(bundleRefs([]byte(early+assignments(maxBundleMatches-1)+late), maxBundleChunks))
	if want := []string{"GET /first/a script_call", "UNKNOWN /x static_literal"}; !reflect.DeepEqual(truncated, want) {
		t.Fatalf("got=%q want=%q", truncated, want)
	}
	// late의 대입까지 합쳐 한도보다 하나 적으면 잘리지 않았으므로 정상 해석한다.
	whole := refStrings(bundleRefs([]byte(early+assignments(maxBundleMatches-3)+late), maxBundleChunks))
	if want := []string{"GET /first/a script_call", "GET /later/x script_call", "GET /later script_call"}; !reflect.DeepEqual(whole, want) {
		t.Fatalf("got=%q want=%q", whole, want)
	}
}

func allChunks(body []byte) []bundleRef {
	sure, guessed := bundleChunks(body)
	return append(sure, guessed...)
}

func chunkStrings(refs []bundleRef) []string {
	out := []string{}
	for _, r := range refs {
		out = append(out, r.path+" "+r.source)
	}
	return out
}

func TestBundleChunks(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want []string
	}{
		"import":          {`import("./chunk-a.js")`, []string{"./chunk-a.js bundle_chunk"}},
		"spaced import":   {`import( "/static/lazy.js")`, []string{"/static/lazy.js bundle_chunk"}},
		"relative string": {`"./chunk-a.js"`, []string{"./chunk-a.js bundle_string"}},
		"bare directory":  {`["assets/b.mjs"]`, []string{"/assets/b.mjs bundle_string"}},
		"vite hashed":     {`["assets/index-a1B2c3D4.js"]`, []string{"/assets/index-a1B2c3D4.js bundle_chunk"}},
		"cra chunk":       {`"static/js/2.abc12345.chunk.js"`, []string{"/static/js/2.abc12345.chunk.js bundle_chunk"}},
		"hash no digit":   {`"assets/application-bootstrap.js"`, []string{"/assets/application-bootstrap.js bundle_string"}},
		"short hash":      {`"assets/index-a1.js"`, []string{"/assets/index-a1.js bundle_string"}},
		"public base":     {`r.p="/app/";x=["assets/b.js"]`, []string{"/app/assets/b.js bundle_string"}},
		"absolute":        {`"/static/c.js"`, []string{"/static/c.js bundle_string"}},
		"bare module":     {`require("moment.js")`, []string{}},
		"node modules":    {`"node_modules/x/y.js"`, []string{}},
		"map":             {`r.u=e=>({1:"abc",2:"def"})[e]+".js"`, []string{"abc.js bundle_chunk", "def.js bundle_chunk"}},
		"id and hash":     {`r.u=e=>e+"."+{7:"abc"}[e]+".js"`, []string{"7.abc.js bundle_chunk"}},
		"prefix":          {`r.u=e=>"chunk-"+{7:"abc"}[e]+".js"`, []string{"chunk-abc.js bundle_chunk"}},
		"public path":     {`r.p="/static/js/",r.u=e=>({7:"abc"})[e]+".js"`, []string{"/static/js/abc.js bundle_chunk"}},
		"duplicate":       {`"/a/b.js","/a/b.js"`, []string{"/a/b.js bundle_string"}},
		"import wins":     {`"/a/lazy.js";import("/a/lazy.js")`, []string{"/a/lazy.js bundle_chunk"}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := chunkStrings(allChunks([]byte(tc.body))); !reflect.DeepEqual(got, tc.want) {
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
	if got := bundleRefs([]byte(b.String()), maxBundleChunks+40); len(got) != maxBundleChunks {
		t.Fatal(len(got))
	}
	if got := bundleRefs([]byte(b.String()), 3); len(got) != 3 {
		t.Fatal(len(got))
	}
	if got := bundleRefs([]byte(b.String()), -2); len(got) != 0 {
		t.Fatal(len(got))
	}
}

func TestBundleChunksPreferConfirmedOverGuessed(t *testing.T) {
	got := chunkStrings(bundleRefs([]byte(`"/guess/a.js","/guess/b.js";import("./real.js")`), 1))
	if want := []string{"./real.js bundle_chunk"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestGuessedScriptsComeAfterAPIRefsInsideChunkBudget(t *testing.T) {
	got := chunkStrings(bundleRefs([]byte(`"/g/a.js","/g/b.js";import("./real.js");c.get("/api/x")`), 2))
	if want := []string{"./real.js bundle_chunk", "/api/x script_call", "/g/a.js bundle_string"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestBundleChunkIsScriptKind(t *testing.T) {
	refs := bundleRefs([]byte(`"/static/c.js"`), maxBundleChunks)
	if len(refs) != 1 || refs[0] != (bundleRef{"GET", "/static/c.js", "script", "bundle_string"}) {
		t.Fatalf("%+v", refs)
	}
}

func TestChunksKeepSlotsAheadOfAPIRefs(t *testing.T) {
	c, _ := flowFixture(t, Manual)
	c.mu.Lock()
	defer c.mu.Unlock()
	var b strings.Builder
	for i := 0; i < c.binding.Limits.MaxEndpoints*2; i++ {
		b.WriteString(`c.get("/read/a` + strconv.Itoa(i) + `");`)
	}
	b.WriteString(`import("./lazy.js");`)
	c.discoverBodyLocked([]byte(b.String()), "application/javascript", "/read/app.js")
	if got := discovered(c); !slices.Contains(got, "GET /read/lazy.js script") {
		t.Fatal(got)
	}
}

func TestChunksCannotCrowdOutAPIRefs(t *testing.T) {
	c, _ := flowFixture(t, Manual)
	c.mu.Lock()
	defer c.mu.Unlock()
	var b strings.Builder
	for i := 0; i < c.binding.Limits.MaxEndpoints*2; i++ {
		b.WriteString(`"/read/x/c` + strconv.Itoa(i) + `.js",`)
	}
	b.WriteString(`c.get("/read/api");`)
	c.discoverBodyLocked([]byte(b.String()), "application/javascript", "/read/app.js")
	scripts, got := 0, discovered(c)
	for _, e := range got {
		if strings.HasSuffix(e, " script") {
			scripts++
		}
	}
	if scripts != c.binding.Limits.MaxEndpoints/2 || !slices.Contains(got, "GET /read/api api") {
		t.Fatal(scripts, got)
	}
}

func TestGuessedScriptsDoNotTakeSlotsBeforeAPIRefs(t *testing.T) {
	c, _ := flowFixture(t, Manual)
	c.mu.Lock()
	defer c.mu.Unlock()
	var b strings.Builder
	for i := 0; i < 16; i++ {
		b.WriteString(`"/read/x/g` + strconv.Itoa(i) + `.js",`)
	}
	for i := 0; i < c.binding.Limits.MaxEndpoints; i++ {
		b.WriteString(`c.get("/read/a` + strconv.Itoa(i) + `");`)
	}
	c.discoverBodyLocked([]byte(b.String()), "application/javascript", "/read/app.js")
	for _, e := range discovered(c) {
		if strings.HasSuffix(e, " script") {
			t.Fatal(e)
		}
	}
}

func TestScriptSlotsAreSharedAcrossBundles(t *testing.T) {
	c, _ := flowFixture(t, Manual)
	c.mu.Lock()
	defer c.mu.Unlock()
	limit := c.binding.Limits.MaxEndpoints / 2
	bundle := func(tag string) []byte {
		var b strings.Builder
		for i := 0; i < limit+4; i++ {
			b.WriteString(`import("/read/` + tag + strconv.Itoa(i) + `.js");`)
		}
		b.WriteString(`c.get("/read/api-` + tag + `");`)
		return []byte(b.String())
	}
	c.discoverBodyLocked(bundle("a"), "application/javascript", "/read/a.js")
	c.discoverBodyLocked(bundle("b"), "application/javascript", "/read/b.js")
	scripts, got := 0, discovered(c)
	for _, e := range got {
		if strings.HasSuffix(e, " script") {
			scripts++
		}
	}
	if scripts != limit || !slices.Contains(got, "GET /read/api-a api") || !slices.Contains(got, "GET /read/api-b api") {
		t.Fatal(scripts, got)
	}
}

func TestConfirmedScriptUpgradesGuessedString(t *testing.T) {
	c, _ := flowFixture(t, Manual)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.addLocked("GET", "/read/lazy.js", "script", "bundle_string", "/read/a.js")
	c.addLocked("GET", "/read/lazy.js", "script", "bundle_chunk", "/read/b.js")
	if e := endpointAt(c, "GET", "/read/lazy.js"); e.Source != "bundle_chunk" || crawlRank(e) != 1 {
		t.Fatalf("%+v", e)
	}
	c.addLocked("GET", "/read/api", "api", "script_call", "/read/a.js")
	c.addLocked("GET", "/read/api", "script", "bundle_chunk", "/read/b.js")
	if e := endpointAt(c, "GET", "/read/api"); e.Source != "script_call" {
		t.Fatalf("%+v", e)
	}
}

func TestBareChunkPathResolvesFromRootNotBundle(t *testing.T) {
	c, _ := flowFixture(t, Manual)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.binding.PathPrefixes = []string{"/"}
	c.discoverBodyLocked([]byte(`["assets/index-a1B2c3D4.js"]`), "application/javascript", "/assets/index.js")
	got := discovered(c)
	if !slices.Contains(got, "GET /assets/index-a1B2c3D4.js script") || slices.Contains(got, "GET /assets/assets/index-a1B2c3D4.js script") {
		t.Fatal(got)
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
	c.discoverBodyLocked([]byte(`<link rel="modulepreload" href="/read/a.js"><link rel="PRELOAD" as="script" href="/read/b.js"><link rel=" preload  foo " as="script" href="/read/e.js"><link rel="preload" as="style" href="/read/c.css"><link rel="stylesheet" href="/read/d.css"><link rel="modulepreload">`), "text/html", "/read")
	got := discovered(c)
	if !slices.Contains(got, "GET /read/a.js script") || !slices.Contains(got, "GET /read/b.js script") || !slices.Contains(got, "GET /read/e.js script") || slices.Contains(got, "GET /read/c.css script") || slices.Contains(got, "GET /read/d.css script") {
		t.Fatal(got)
	}
	if e := endpointAt(c, "GET", "/read"); e.Kind == "script" || e.Duplicates != 0 {
		t.Fatalf("empty href reached the page itself: %+v", e)
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

func TestDiscoveryResolvesAbsoluteAndRelativeCalls(t *testing.T) {
	c, _ := flowFixture(t, Manual)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.addLocked("GET", "/read/index", "page", "html_link", "/read")
	c.addLocked("GET", "/read/js/app.js", "script", "html_script", "/read/index")
	invalid := c.record.Snapshot.Counts.InvalidPaths
	c.discoverBodyLocked([]byte(`c.get("https://owned.test/read/abs");c.get("rel/x");this.http.get("old");c.get("https://other.test/read/y")`), "application/javascript", "/read/js/app.js")
	got := discovered(c)
	if !slices.Contains(got, "GET /read/abs api") || !slices.Contains(got, "GET /read/rel/x api") || !slices.Contains(got, "GET /read/old api") || slices.Contains(got, "GET /read/y api") || slices.Contains(got, "GET /read/js/rel/x api") {
		t.Fatal(got)
	}
	if c.record.Snapshot.Counts.InvalidPaths != invalid+1 {
		t.Fatal(c.record.Snapshot.Counts.InvalidPaths)
	}
}

func TestRelativeCallFollowsScriptChainToDocument(t *testing.T) {
	c, _ := flowFixture(t, Manual)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.addLocked("GET", "/read/index", "page", "html_link", "/read")
	c.addLocked("GET", "/read/js/app.js", "script", "html_script", "/read/index")
	c.addLocked("GET", "/read/js/lazy/c.js", "script", "bundle_chunk", "/read/js/app.js")
	c.discoverBodyLocked([]byte(`c.get("rel/x")`), "application/javascript", "/read/js/lazy/c.js")
	if e := endpointAt(c, "GET", "/read/rel/x"); e.From != "/read/js/lazy/c.js" {
		t.Fatalf("%+v %q", e, discovered(c))
	}
}

func TestRelativeCallWithoutKnownDocumentIsUnknown(t *testing.T) {
	c, _ := flowFixture(t, Manual)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.discoverBodyLocked([]byte(`c.get("rel/x")`), "application/javascript", "/read/js/app.js")
	got := discovered(c)
	if !slices.Contains(got, "UNKNOWN /read/js/rel/x api") || slices.Contains(got, "GET /read/js/rel/x api") {
		t.Fatal(got)
	}
}

func TestInlineScriptResolvesRelativeCallAgainstItsPage(t *testing.T) {
	c, _ := flowFixture(t, Manual)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.discoverBodyLocked([]byte(`<script>c.get("rel/x")</script>`), "text/html", "/read/index")
	if got := discovered(c); !slices.Contains(got, "GET /read/rel/x api") {
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
		{Endpoint{Kind: "script", Source: "bundle_chunk", Path: "/lazy.js"}, 1},
		{Endpoint{Kind: "script", Source: "bundle_string", Path: "/src/error.js"}, 3},
		{Endpoint{Kind: "page", Path: "/vendor.js"}, 2},
	} {
		if got := crawlRank(tc.e); got != tc.want {
			t.Errorf("%+v got=%d want=%d", tc.e, got, tc.want)
		}
	}
}

func TestCrawlVisitsPagesBeforeGuessedScriptStrings(t *testing.T) {
	c, _ := flowFixture(t, Manual)
	c.binding.Limits.MaxPages = 2
	var order []string
	handled, err := c.Crawl(t.Context(), func(ctx context.Context, target string) (e2runner.Result, error) {
		order = append(order, strings.TrimPrefix(target, "https://owned.test"))
		return crawlResult(`<a href="/read/page">p</a><script>var a="/read/src/error.js"</script>`), nil
	})
	if !handled || err != nil {
		t.Fatal(handled, err)
	}
	if want := []string{"/read", "/read/page"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("got=%q want=%q", order, want)
	}
}

func crawlResult(body string) e2runner.Result {
	return e2runner.Result{ToolOutput: "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\n\r\n" + body, Trusted: e2runner.TrustedResult{Version: e2proxy.Version, Final: true, ProcessDrained: true, ProxyDrained: true, Call: e2proxy.CallResult{Final: true, AuditComplete: true, Counts: e2proxy.Counts{ForwardAttempted: 1}}}}
}

func TestCrawlLeavesUnreadFetchOptionsUnvisitedAndUnselected(t *testing.T) {
	c, _ := flowFixture(t, Auto)
	c.binding.Limits.MaxPages = 3
	var order []string
	handled, err := c.Crawl(t.Context(), func(ctx context.Context, target string) (e2runner.Result, error) {
		order = append(order, strings.TrimPrefix(target, "https://owned.test"))
		return crawlResult(`<script>fetch("/read/nested",{headers:{a:1},method:"POST"});fetch("/read/init",init)</script>`), nil
	})
	if !handled || err != nil {
		t.Fatal(handled, err)
	}
	if want := []string{"/read"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("got=%q want=%q", order, want)
	}
	found := 0
	for _, e := range c.record.Snapshot.Endpoints {
		if e.Path == "/read/nested" || e.Path == "/read/init" {
			found++
			if e.Method != "UNKNOWN" || e.Selected {
				t.Fatalf("%+v", e)
			}
		}
	}
	if found != 2 {
		t.Fatal(found)
	}
}

func TestCrawlVisitsChunkWhenEndpointSlotsAreFull(t *testing.T) {
	c, _ := flowFixture(t, Manual)
	c.binding.Limits.MaxPages = 2
	var calls strings.Builder
	for i := 0; i < c.binding.Limits.MaxEndpoints; i++ {
		calls.WriteString(`c.get("/read/a` + strconv.Itoa(i) + `");`)
	}
	calls.WriteString(`import("/read/chunk.js");`)
	var order []string
	handled, err := c.Crawl(t.Context(), func(ctx context.Context, target string) (e2runner.Result, error) {
		order = append(order, strings.TrimPrefix(target, "https://owned.test"))
		return crawlResult("<script>" + calls.String() + "</script>"), nil
	})
	if !handled || err != nil {
		t.Fatal(handled, err)
	}
	if want := []string{"/read", "/read/chunk.js"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("got=%q want=%q", order, want)
	}
}

func endpointAt(c *Controller, method, path string) Endpoint {
	if e := c.endpointLocked(method, path); e != nil {
		return *e
	}
	return Endpoint{}
}

func TestCrawlLeavesInferredCandidatesUnvisitedAndUnselected(t *testing.T) {
	c, _ := flowFixture(t, Auto)
	c.binding.Limits.MaxPages = 4
	var order []string
	handled, err := c.Crawl(t.Context(), func(ctx context.Context, target string) (e2runner.Result, error) {
		order = append(order, strings.TrimPrefix(target, "https://owned.test"))
		return crawlResult(`<script>this.http.get(this.server+"/read/guess");c.get("/read/items/"+id);c.post("/read/things/"+id)</script>`), nil
	})
	if !handled || err != nil {
		t.Fatal(handled, err)
	}
	if want := []string{"/read"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("got=%q want=%q", order, want)
	}
	if e := endpointAt(c, "GET", "/read/guess"); !e.InScope || e.Change != "possible" || e.Selected {
		t.Fatalf("%+v", e)
	}
	for _, e := range []Endpoint{endpointAt(c, "GET", "/read/items"), endpointAt(c, "POST", "/read/things")} {
		if e.Path == "" || e.InScope || e.Change != "blocked" || e.Selected {
			t.Fatalf("%+v", e)
		}
	}
}

func TestLiteralCallPromotesInferredEndpoint(t *testing.T) {
	c, _ := flowFixture(t, Manual)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.addLocked("GET", "/read/guess", "api", "script_guess", "/read/a.js")
	c.addLocked("GET", "/read/prefix", "api", "script_prefix", "/read/a.js")
	c.addLocked("GET", "/read/guess", "api", "script_call", "/read/b.js")
	c.addLocked("GET", "/read/prefix", "api", "script_guess", "/read/b.js")
	if e := endpointAt(c, "GET", "/read/guess"); e.Source != "script_call" || e.From != "/read/b.js" || e.Change != "attested_read" || !e.InScope {
		t.Fatalf("%+v", e)
	}
	if e := endpointAt(c, "GET", "/read/prefix"); e.Source != "script_prefix" || e.Change != "blocked" {
		t.Fatalf("%+v", e)
	}
	c.addLocked("GET", "/read/form", "form", "html_form", "/read")
	c.addLocked("GET", "/read/form", "api", "script_call", "/read/a.js")
	if e := endpointAt(c, "GET", "/read/form"); e.Kind != "form" || e.Change != "possible" {
		t.Fatalf("%+v", e)
	}
}

func TestBundleLiteralCallReplacesGuessedSource(t *testing.T) {
	got := refStrings(bundleRefs([]byte(`c.get(env.x+"/a");c.get("/a")`), maxBundleChunks))
	if want := []string{"GET /a script_call"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%q want=%q", got, want)
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
