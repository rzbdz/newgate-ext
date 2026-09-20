package archmap

import (
	"encoding/json"
	"html/template"
	"strings"
)

// Render 把整份报告渲成一个**自包含**的 HTML。
//
// 自包含是硬要求：产物要能离线双击打开（开发机、跳板机、发到别人手上都一样），
// 所以没有一个外链——样式、脚本、数据全在文件里。代价是这个文件几百 KB，而它
// 本来就是给 review 用的，不是给人天天加载的网页。
func Render(r Report) (string, error) {
	blob, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	// </script> 出现在数据里会把脚本提前关掉（包路径里不会有，但数据将来会长），
	// 转义一下比信任输入便宜。
	data := strings.ReplaceAll(string(blob), "</", `<\/`)

	var sb strings.Builder
	if err := page.Execute(&sb, map[string]any{
		"Data":  template.JS(data),
		"Count": len(r.Specs),
	}); err != nil {
		return "", err
	}
	return sb.String(), nil
}

var page = template.Must(template.New("archmap").Parse(`<!doctype html>
<html lang="zh-Hans">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>newgate architecture</title>
<style>
:root{
  --bg:#0f1115; --panel:#161a21; --line:#242a34; --ink:#e6e9ef; --dim:#8b93a3;
  --core:#5b8def; --ext:#e0a34a; --cross:#e06c9f; --missing:#6b7280;
  --ok:#4ea87a; --warn:#d9a441; --danger:#d96a6a;
  --r:10px; --shadow:0 6px 24px rgba(0,0,0,.35);
}
@media (prefers-color-scheme: light){
  :root{ --bg:#f6f7f9; --panel:#fff; --line:#e3e6ec; --ink:#1b1f27; --dim:#6b7280;
         --shadow:0 6px 20px rgba(16,24,40,.08); }
}
*{box-sizing:border-box}
html,body{height:100%;margin:0}
body{
  background:var(--bg); color:var(--ink);
  font:13px/1.5 -apple-system,BlinkMacSystemFont,"SF Pro Text","Helvetica Neue",
       "PingFang SC","Microsoft YaHei",system-ui,sans-serif;
  display:flex; flex-direction:column; overflow:hidden;
}
header{
  display:flex; align-items:center; gap:14px; padding:10px 16px;
  background:var(--panel); border-bottom:1px solid var(--line); flex-wrap:wrap;
}
h1{font-size:14px;font-weight:600;margin:0;letter-spacing:.2px}
h1 small{color:var(--dim);font-weight:400;margin-left:8px}
.tabs{display:flex;gap:4px;background:var(--bg);padding:3px;border-radius:9px}
.tab{
  padding:5px 14px;border-radius:7px;cursor:pointer;border:0;background:transparent;
  color:var(--dim);font:inherit;font-weight:500;transition:all .18s ease;
}
.tab:hover{color:var(--ink)}
.tab[aria-selected="true"]{background:var(--panel);color:var(--ink);box-shadow:var(--shadow)}
.spacer{flex:1}
select,input[type=search]{
  background:var(--bg);color:var(--ink);border:1px solid var(--line);
  border-radius:8px;padding:5px 10px;font:inherit;outline:none;
}
input[type=search]{width:190px}
input[type=search]:focus,select:focus{border-color:var(--core)}
label.chk{display:flex;align-items:center;gap:6px;color:var(--dim);cursor:pointer;user-select:none}
label.chk input{accent-color:var(--core)}
main{flex:1;display:flex;min-height:0}
#canvas{flex:1;position:relative;overflow:hidden;cursor:grab}
#canvas.dragging{cursor:grabbing}
#svg{width:100%;height:100%;display:block}
#panel{
  width:290px;background:var(--panel);border-left:1px solid var(--line);
  padding:14px;overflow:auto;
}
#panel h2{font-size:12px;text-transform:uppercase;letter-spacing:.7px;color:var(--dim);margin:0 0 8px}
#panel .note{color:var(--dim);font-size:12px}
.kv{display:flex;justify-content:space-between;gap:10px;padding:3px 0;border-bottom:1px dashed var(--line)}
.kv:last-child{border:0}
.kv b{font-weight:500;color:var(--dim)}
.chip{display:inline-block;padding:1px 7px;border-radius:99px;font-size:11px;border:1px solid var(--line);color:var(--dim)}
.legend{display:flex;flex-wrap:wrap;gap:6px;margin-top:10px}
.legend span{display:flex;align-items:center;gap:5px;font-size:11px;color:var(--dim)}
.dot{width:9px;height:9px;border-radius:3px;display:inline-block}
.node rect{rx:8;ry:8;stroke-width:1.5;transition:filter .15s ease}
.node text{font:12px ui-monospace,SFMono-Regular,Menlo,monospace;fill:var(--ink);
           dominant-baseline:middle;pointer-events:none}
.node{cursor:pointer}
.node.dim{opacity:.16}
.node.hot rect{filter:drop-shadow(0 0 10px rgba(91,141,239,.75))}
.edge{fill:none;stroke-width:1.4;transition:opacity .15s ease}
.edge.dim{opacity:.07}
.edge.hot{stroke-width:2.6}
.band{fill:var(--line);opacity:.28}
.bandlabel{font:10px ui-monospace,monospace;fill:var(--dim)}
.hint{position:absolute;left:14px;bottom:12px;color:var(--dim);font-size:11px;
      background:var(--panel);padding:5px 10px;border-radius:8px;border:1px solid var(--line)}
</style>
</head>
<body>
<header>
  <h1>newgate architecture<small id="subtitle"></small></h1>
  <div class="tabs" role="tablist">
    <button class="tab" role="tab" id="tab-resolve">assembly (resolve)</button>
    <button class="tab" role="tab" id="tab-imports">imports (scanned)</button>
  </div>
  <select id="spec" title="distribution spec"></select>
  <div class="spacer"></div>
  <input type="search" id="search" placeholder="find a module / package (enter)">
  <label class="chk"><input type="checkbox" id="opt" checked>optional edges</label>
  <label class="chk"><input type="checkbox" id="grid" checked>layer bands</label>
</header>
<main>
  <div id="canvas">
    <svg id="svg"><g id="viewport"></g></svg>
    <div class="hint">wheel to zoom · drag to pan · click a node to pin · double-click to reset</div>
  </div>
  <aside id="panel">
    <h2 id="ptitle">overview</h2>
    <div id="pbody" class="note">hover a node.</div>
  </aside>
</main>
<script>
const DATA = {{.Data}};
const NS = "http://www.w3.org/2000/svg";

const GROUPS = {
  // assembly graph: coloured by module type
  infra:{c:"#5b8def",t:"infra (mechanism)"}, gateway:{c:"#4ea87a",t:"gateway (data plane)"},
  cli:{c:"#e0a34a",t:"cli / ui (interface)"}, config:{c:"#9b7bd4",t:"config"},
  runtime:{c:"#d96a6a",t:"runtime (takeover)"}, module:{c:"#6b7280",t:"other modules"},
  missing:{c:"#6b7280",t:"not installed here"},
  // import graph: coloured by repo
  core:{c:"#5b8def",t:"core (kernel)"}, ext:{c:"#e0a34a",t:"ext (distribution)"},
};
const EDGE_STYLE = {
  solid:{stroke:"#7d8899",dash:""},          // Need
  dashed:{stroke:"#7d8899",dash:"5 4"},      // Optional
  cross:{stroke:"#e06c9f",dash:""},          // crosses the replace boundary
};

let state = {tab:"resolve", spec:0, focus:null, graph:null, showOpt:true, showBands:true};
let view = {x:0, y:0, k:1};

function currentGraph(){
  if(state.tab === "imports") return DATA.imports;
  return DATA.specs[state.spec] || DATA.specs[0];
}

function el(tag, attrs){
  const n = document.createElementNS(NS, tag);
  for(const k in attrs) n.setAttribute(k, attrs[k]);
  return n;
}

function draw(){
  const g = currentGraph();
  state.graph = g;
  state.focus = null;
  const vp = document.getElementById("viewport");
  vp.replaceChildren();
  document.getElementById("subtitle").textContent = g.title + " · " + g.nodes.length + " nodes / " + g.edges.length + " edges";
  document.getElementById("spec").style.display = state.tab === "resolve" ? "" : "none";

  const byId = {};
  g.nodes.forEach(n => byId[n.id] = n);

  // Layer bands. When reviewing, depth is the most useful thing on screen:
  // everything on one band knows nothing about the rest; deps only cross bands.
  if(state.showBands){
    for(let l = 0; l < g.layers; l++){
      const y = g.nodes.find(n => n.layer === l)?.y;
      if(y === undefined) continue;
      vp.appendChild(el("rect", {class:"band", x:0, y:y-14, width:g.width, height:60, rx:8}));
      vp.appendChild(text(20, y+16, "L" + l, "bandlabel"));
    }
  }

  const edgeLayer = el("g", {});
  const nodeLayer = el("g", {});
  vp.appendChild(edgeLayer); vp.appendChild(nodeLayer);

  const edges = [];
  g.edges.forEach(e => {
    const a = byId[e.from], b = byId[e.to];
    if(!a || !b) return;
    if(e.style === "dashed" && !state.showOpt) return;
    const path = el("path", {class:"edge", d:curve(a,b), stroke:EDGE_STYLE[e.style].stroke,
                             "stroke-dasharray":EDGE_STYLE[e.style].dash, "marker-end":"url(#arrow)"});
    path.dataset.from = e.from; path.dataset.to = e.to;
    edgeLayer.appendChild(path);
    edges.push(path);
  });

  g.nodes.forEach(n => {
    const grp = el("g", {class:"node", transform:"translate(" + n.x + "," + n.y + ")"});
    const color = (GROUPS[n.group] || GROUPS.module).c;
    grp.appendChild(el("rect", {width:n.w, height:n.h, x:0, y:0,
      fill: n.missing ? "transparent" : "color-mix(in srgb, " + color + " 16%, transparent)",
      stroke: color, "stroke-dasharray": n.missing ? "4 3" : ""}));
    grp.appendChild(text(n.w/2, n.h/2, n.label, "", "middle"));
    const tip = el("title", {});
    tip.textContent = n.label + (n.note ? "\n" + n.note : "");
    grp.appendChild(tip);
    grp.addEventListener("mouseenter", () => highlight(n.id));
    grp.addEventListener("mouseleave", () => { if(!state.focus) highlight(null); });
    grp.addEventListener("click", ev => { ev.stopPropagation(); focus(n.id); });
    grp.dataset.id = n.id;
    nodeLayer.appendChild(grp);
  });

  const svg = document.getElementById("svg");
  if(!svg.querySelector("#arrow")){
    const defs = el("defs", {});
    defs.innerHTML = '<marker id="arrow" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="7" ' +
      'markerHeight="7" orient="auto"><path d="M0,0 L8,4 L0,8 z" fill="#7d8899"/></marker>';
    svg.insertBefore(defs, vp);
  }
  fit();
}

function text(x, y, s, cls, anchor){
  const t = el("text", {x:x, y:y});
  if(cls) t.setAttribute("class", cls);
  if(anchor) t.setAttribute("text-anchor", anchor);
  t.textContent = s;
  return t;
}

// curve goes from the bottom edge of the upper node to the top edge of the lower
// one: edges point upwards, so the arrow lands on the node being depended on.
function curve(a, b){
  const x1 = a.x + a.w/2, y1 = a.y + a.h;
  const x2 = b.x + b.w/2, y2 = b.y;
  if(y2 - y1 < 6){            // same layer or reversed: arc around, do not cut through
    const mx = (x1 + x2)/2 + (x1 < x2 ? -60 : 60);
    return "M" + x1 + "," + y1 + " C" + mx + "," + (y1+40) + " " + mx + "," + (y2-40) + " " + x2 + "," + y2;
  }
  const dy = (y2 - y1) * 0.45;
  return "M" + x1 + "," + y1 + " C" + x1 + "," + (y1+dy) + " " + x2 + "," + (y2-dy) + " " + x2 + "," + y2;
}

function neighbors(id){
  const g = state.graph, out = {deps:new Set(), users:new Set()};
  g.edges.forEach(e => {
    if(e.from === id) out.deps.add(e.to);
    if(e.to === id) out.users.add(e.from);
  });
  return out;
}

function highlight(id){
  const nodes = document.querySelectorAll(".node");
  const edges = document.querySelectorAll(".edge");
  if(!id){
    nodes.forEach(n => n.classList.remove("dim","hot"));
    edges.forEach(e => e.classList.remove("dim","hot"));
    if(!state.focus) showPanel(null);
    return;
  }
  const nb = neighbors(id);
  nodes.forEach(n => {
    const mine = n.dataset.id === id || nb.deps.has(n.dataset.id) || nb.users.has(n.dataset.id);
    n.classList.toggle("dim", !mine);
    n.classList.toggle("hot", n.dataset.id === id);
  });
  edges.forEach(e => {
    const mine = e.dataset.from === id || e.dataset.to === id;
    e.classList.toggle("dim", !mine);
    e.classList.toggle("hot", mine);
  });
  showPanel(id);
}

function focus(id){
  state.focus = id;
  highlight(id);
  const n = state.graph.nodes.find(x => x.id === id);
  panTo(n.x + n.w/2, n.y + n.h/2);
}

function showPanel(id){
  const title = document.getElementById("ptitle");
  const body = document.getElementById("pbody");
  if(!id){
    title.textContent = "overview";
    const g = state.graph;
    body.innerHTML = '<div class="note">' + g.note + '</div>' +
      '<div class="kv"><b>nodes</b><span>' + g.nodes.length + '</span></div>' +
      '<div class="kv"><b>edges</b><span>' + g.edges.length + '</span></div>' +
      '<div class="kv"><b>layers</b><span>' + g.layers + '</span></div>' +
      legend();
    return;
  }
  const n = state.graph.nodes.find(x => x.id === id);
  const nb = neighbors(id);
  const byId = {}; state.graph.nodes.forEach(x => byId[x.id] = x);
  title.textContent = n.label;
  let html = '<div class="note">' + (n.note || "") + '</div>';
  html += '<div class="kv"><b>layer</b><span>L' + n.layer + '</span></div>';
  html += '<div class="kv"><b>depends on</b><span>' + nb.deps.size + '</span></div>';
  html += '<div class="kv"><b>depended on by</b><span>' + nb.users.size + '</span></div>';
  if(nb.deps.size) html += list("depends on", [...nb.deps].map(x => byId[x]?.label || x));
  if(nb.users.size) html += list("depended on by", [...nb.users].map(x => byId[x]?.label || x));
  body.innerHTML = html;
}

function list(head, items){
  return '<h2 style="margin:12px 0 6px">' + head + '</h2><div class="note">' +
    items.map(i => '<div class="kv"><span>' + i + '</span></div>').join("") + '</div>';
}

function legend(){
  const seen = {};
  state.graph.nodes.forEach(n => seen[n.group] = true);
  return '<div class="legend">' + Object.keys(seen).sort().map(k =>
    '<span><i class="dot" style="background:' + (GROUPS[k]||GROUPS.module).c + '"></i>' +
    (GROUPS[k]||GROUPS.module).t + '</span>').join("") + '</div>';
}

// ---------- viewport ----------

function applyView(){
  document.getElementById("viewport").setAttribute("transform",
    "translate(" + view.x + "," + view.y + ") scale(" + view.k + ")");
}
function fit(){
  const g = state.graph, c = document.getElementById("canvas").getBoundingClientRect();
  const k = Math.min((c.width - 40)/g.width, (c.height - 40)/g.height, 1.15);
  view.k = k > 0 ? k : 1;
  view.x = (c.width - g.width*view.k)/2;
  view.y = (c.height - g.height*view.k)/2;
  applyView();
}
function panTo(x, y){
  const c = document.getElementById("canvas").getBoundingClientRect();
  view.x = c.width/2 - x*view.k;
  view.y = c.height/2 - y*view.k;
  applyView();
}

const canvas = document.getElementById("canvas");
canvas.addEventListener("wheel", ev => {
  ev.preventDefault();
  const r = canvas.getBoundingClientRect();
  const mx = ev.clientX - r.left, my = ev.clientY - r.top;
  const k2 = Math.max(0.15, Math.min(3, view.k * (ev.deltaY < 0 ? 1.1 : 0.9)));
  view.x = mx - (mx - view.x) * (k2/view.k);
  view.y = my - (my - view.y) * (k2/view.k);
  view.k = k2; applyView();
}, {passive:false});
let drag = null;
canvas.addEventListener("mousedown", ev => { drag = {x:ev.clientX, y:ev.clientY, vx:view.x, vy:view.y};
  canvas.classList.add("dragging"); });
window.addEventListener("mousemove", ev => {
  if(!drag) return;
  view.x = drag.vx + (ev.clientX - drag.x);
  view.y = drag.vy + (ev.clientY - drag.y);
  applyView();
});
window.addEventListener("mouseup", () => { drag = null; canvas.classList.remove("dragging"); });
canvas.addEventListener("dblclick", () => { state.focus = null; highlight(null); fit(); });
canvas.addEventListener("click", () => { state.focus = null; highlight(null); });

// ---------- interaction ----------

document.getElementById("tab-resolve").addEventListener("click", () => setTab("resolve"));
document.getElementById("tab-imports").addEventListener("click", () => setTab("imports"));
function setTab(t){
  state.tab = t;
  document.getElementById("tab-resolve").setAttribute("aria-selected", t === "resolve");
  document.getElementById("tab-imports").setAttribute("aria-selected", t === "imports");
  draw();
}
document.getElementById("spec").addEventListener("change", ev => { state.spec = +ev.target.value; draw(); });
document.getElementById("opt").addEventListener("change", ev => { state.showOpt = ev.target.checked; draw(); });
document.getElementById("grid").addEventListener("change", ev => { state.showBands = ev.target.checked; draw(); });
document.getElementById("search").addEventListener("keydown", ev => {
  if(ev.key !== "Enter") return;
  const q = ev.target.value.trim().toLowerCase();
  if(!q) return;
  const hit = state.graph.nodes.find(n => n.label.toLowerCase().includes(q));
  if(hit) focus(hit.id);
});

DATA.specs.forEach((s, i) => {
  const o = document.createElement("option");
  o.value = i; o.textContent = s.title;
  document.getElementById("spec").appendChild(o);
});
setTab("resolve");
</script>
</body>
</html>
`))
