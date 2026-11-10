package main

const (
	htmlType  = "text/html; charset=utf-8"
	jsType    = "text/javascript"
	pageHead  = `<!doctype html><meta charset="utf-8"><title>fixture</title>`
	itemsJSON = `{"items":["first","second"]}`
	appCSS    = `#app{font-family:sans-serif}`
	logoSVG   = `<svg xmlns="http://www.w3.org/2000/svg" width="8" height="8"><rect width="8" height="8"/></svg>`
)

// 첫 화면은 JS가 API를 읽어 그린다. 정적 HTML만 읽으면 내용이 없다.
const indexHTML = pageHead + `<link rel="stylesheet" href="/assets/app.css"><div id="app"></div><script src="/assets/app.js"></script>`

const appJS = `fetch("/api/items").then(r=>r.json()).then(d=>{
document.getElementById("app").innerHTML='<h1 id="fixture-ready">ready</h1><img src="/assets/logo.svg"><ul>'+d.items.map(i=>"<li>"+i+"</li>").join("")+"</ul>"
})`

const serviceWorkerJS = `self.addEventListener("install",e=>e.waitUntil(fetch("{{R}}/hit/B06-install",{mode:"no-cors"}).catch(()=>{})));
self.addEventListener("activate",e=>e.waitUntil(clients.claim()));
self.addEventListener("fetch",e=>{if(new URL(e.request.url).pathname==="/sw-probe"){e.respondWith(fetch("{{R}}/hit/B06-fetch",{mode:"no-cors"}))}});`

// 키는 수용시험 문서의 시험 ID다.
var scenarios = map[string]string{
	"B04": `<img src="{{R}}/hit/B04-img"><script src="{{R}}/hit/B04-script"></script><script>fetch("{{R}}/hit/B04-fetch",{mode:"no-cors"})</script>`,
	"B05": `<iframe src="/out/B05-frame"></iframe><script>fetch("/out/B05-fetch",{mode:"no-cors"})</script>`,
	"B06": `<script>const s=navigator.serviceWorker;if(s.controller){fetch("/sw-probe")}s.addEventListener("controllerchange",()=>fetch("/sw-probe"));s.register("/sw.js")</script>`,
	"B07": `<script>new WebSocket("{{WS}}/hit/B07")</script>`,
	"B08": `<script>for(let i=0;i<3;i++){fetch("/api/items")}</script>`,
	"B09": `<script>const c=new RTCPeerConnection({iceServers:[{urls:"stun:{{STUN}}"}]});c.createDataChannel("x");c.createOffer().then(o=>c.setLocalDescription(o))</script>`,
	"B12": `<link rel="preconnect" href="{{R}}"><link rel="dns-prefetch" href="{{R}}"><link rel="prefetch" href="{{R}}/hit/B12-prefetch">`,
	"B13": `<script>new Worker(URL.createObjectURL(new Blob(['fetch("{{R}}/hit/B13-blob",{mode:"no-cors"})'],{type:"text/javascript"})));new Worker("data:text/javascript,"+encodeURIComponent('fetch("{{R}}/hit/B13-data",{mode:"no-cors"})'))</script>`,
	"B14": `<form id="f" method="post" action="/write/B14-form"></form><script>fetch("/write/B14-fetch",{method:"POST",body:"x"});navigator.sendBeacon("/write/B14-beacon","x");document.getElementById("f").submit()</script>`,
}

// 쓰기까지 승인한 크롤링(S13)의 시도다. 어떤 쓰기가 닿아야 하는지는 회차 정책이 정하고, 이 페이지는 메서드·경로별로
// 이름이 다른 시도만 만든다. 페이지가 스스로 보내는 쓰기와, 크롤러가 직접 제출해야 하는 폼(S13-click)을 함께 둔다.
// remove-S13·S13-excluded는 회차 정책의 위험 경로·제외 경로에 넣는다(docs/evaluation/browser-acceptance.md S13).
// 시도끼리 서로 기다리지 않는다. 한 요청이 막히거나 응답이 오지 않아도 다른 시도는 나간다(#377 리뷰).
// 자동 폼은 스크립트가 만들어 숨은 iframe으로 바로 제출하고 지워서, 페이지가 이동하지 않고 크롤러가 다시 찾지 못한다.
// 모든 시도를 보낸 뒤 #s13-attempted에 시도 수를 적어, 페이지 스크립트가 돌았는지 따로 확인할 수 있게 한다.
var crawlScenarios = map[string]string{
	"S13": `<iframe name="s13-sink" hidden></iframe>` +
		`<form method="post" action="/write/S13-click"><input name="note" value="x"><button id="save">save</button></form>` +
		`<output id="s13-attempted"></output>` +
		`<script>const f=document.createElement("form");f.method="post";f.action="/write/S13-form";f.target="s13-sink";` +
		`document.body.appendChild(f);f.submit();f.remove();let n=1;` +
		`const send=(m,p)=>{n++;try{fetch(p,{method:m,body:m==="DELETE"?undefined:"x"}).catch(()=>{})}catch(e){}};` +
		`send("POST","/write/S13-post");send("PUT","/write/S13-put");send("PATCH","/write/S13-patch");` +
		`send("DELETE","/write/S13-delete");send("POST","/write/remove-S13");send("POST","/write/S13-excluded");` +
		`document.getElementById("s13-attempted").textContent=String(n)</script>`,
}

var (
	redirectHits = map[string]bool{"B05-frame": true, "B05-fetch": true}
	// 쓰기 시도 이름과 페이지가 쓰는 메서드. 메서드가 다르면 그 이름으로 세지 않는다.
	writeHits = map[string]string{"B14-form": "POST", "B14-fetch": "POST", "B14-beacon": "POST",
		"S13-form": "POST", "S13-click": "POST", "S13-post": "POST", "S13-put": "PUT", "S13-patch": "PATCH",
		"S13-delete": "DELETE", "remove-S13": "POST", "S13-excluded": "POST"}
	receiverHits = map[string]bool{
		"B04-img": true, "B04-script": true, "B04-fetch": true, "B05-frame": true, "B05-fetch": true,
		"B06-install": true, "B06-fetch": true, "B07": true, "B12-prefetch": true, "B13-blob": true, "B13-data": true,
	}
)
