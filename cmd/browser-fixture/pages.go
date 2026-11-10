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

var (
	redirectHits = map[string]bool{"B05-frame": true, "B05-fetch": true}
	writeHits    = map[string]bool{"B14-form": true, "B14-fetch": true, "B14-beacon": true}
	receiverHits = map[string]bool{
		"B04-img": true, "B04-script": true, "B04-fetch": true, "B05-frame": true, "B05-fetch": true,
		"B06-install": true, "B06-fetch": true, "B07": true, "B12-prefetch": true, "B13-blob": true, "B13-data": true,
	}
)
