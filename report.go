package chash

import (
	"encoding/json"
	"os"
	"strings"
)

// WriteHTMLReport renders rep as a single self-contained static HTML file
// (no frameworks, no external resources) at path.
func WriteHTMLReport(path string, rep *SimReport) error {
	data, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	// Guard against the JSON accidentally terminating the script tag.
	safe := strings.ReplaceAll(string(data), "</", "<\\/")
	html := strings.Replace(reportTemplate, "__REPORT_JSON__", safe, 1)
	return os.WriteFile(path, []byte(html), 0o644)
}

const reportTemplate = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<title>一致性哈希热点再平衡报告</title>
<style>
  :root { --bg:#0f1420; --panel:#1a2233; --fg:#dfe6f2; --muted:#8a94a8; --accent:#e5533d; }
  * { box-sizing: border-box; }
  body { margin:0; background:var(--bg); color:var(--fg);
         font-family:-apple-system,"PingFang SC","Helvetica Neue",Arial,sans-serif; }
  header { padding:18px 28px 8px; }
  header h1 { font-size:20px; margin:0 0 4px; }
  header p { margin:0; color:var(--muted); font-size:13px; }
  .layout { display:flex; flex-wrap:wrap; gap:16px; padding:16px 28px 28px; }
  .panel { background:var(--panel); border-radius:10px; padding:14px 16px; }
  #ringPanel { flex:0 0 auto; }
  #sidePanel { flex:1 1 380px; min-width:340px; display:flex; flex-direction:column; gap:14px; }
  .controls { display:flex; align-items:center; gap:10px; }
  .controls input[type=range] { flex:1; }
  button { background:#2b3a55; color:var(--fg); border:0; border-radius:6px;
           padding:6px 14px; cursor:pointer; font-size:13px; }
  button:hover { background:#3a4d70; }
  table { border-collapse:collapse; width:100%; font-size:12.5px; }
  th, td { text-align:left; padding:5px 8px; border-bottom:1px solid #2a3450; }
  th { color:var(--muted); font-weight:600; }
  #eventLog { list-style:none; margin:0; padding:0; max-height:300px; overflow-y:auto; font-size:12.5px; }
  #eventLog li { padding:5px 8px; border-bottom:1px solid #232c44; }
  #eventLog .ep { color:var(--muted); display:inline-block; width:64px; }
  .tag { display:inline-block; padding:1px 7px; border-radius:8px; font-size:11px; margin-right:6px; }
  .tag.split_start { background:#5a2d22; color:#ff9d8a; }
  .tag.split_done { background:#234d33; color:#8be0a8; }
  .tag.node_add { background:#1f3a5f; color:#8fc1ff; }
  .tag.node_remove { background:#4d2440; color:#f0a4d8; }
  h2 { font-size:14px; margin:0 0 8px; color:var(--muted); font-weight:600; }
  #heatPanel { flex:1 1 100%; }
  #heatmap { width:100%; height:140px; display:block; }
  .legend { display:flex; flex-wrap:wrap; gap:10px; font-size:12px; margin-top:8px; }
  .legend span { display:inline-flex; align-items:center; gap:5px; }
  .sw { width:12px; height:12px; border-radius:3px; display:inline-block; }
  #summary { display:flex; gap:22px; flex-wrap:wrap; font-size:13px; }
  #summary b { font-size:17px; display:block; }
</style>
</head>
<body>
<header>
  <h1>热点感知的一致性哈希分片 — 运行报告</h1>
  <p>哈希环（虚拟节点）+ 热点检测 + 动态拆分迁移。拖动时间轴查看各 epoch 的访问热度与迁移路径。</p>
</header>
<div class="layout">
  <div class="panel" id="ringPanel">
    <h2>哈希环分片分布与迁移路径</h2>
    <svg id="ring" width="620" height="620" viewBox="0 0 620 620"></svg>
    <div class="legend" id="nodeLegend"></div>
  </div>
  <div id="sidePanel">
    <div class="panel">
      <h2>时间轴</h2>
      <div class="controls">
        <button id="playBtn">播放</button>
        <input type="range" id="epochSlider" min="1" value="1">
        <span id="epochLabel"></span>
      </div>
    </div>
    <div class="panel">
      <h2>运行摘要</h2>
      <div id="summary"></div>
    </div>
    <div class="panel">
      <h2>节点增删迁移比例校验（理论 ≈ 1/N）</h2>
      <table id="ratioTable">
        <thead><tr><th>事件</th><th>迁移 key 数</th><th>实测比例</th><th>理论比例(1/N)</th><th>仅涉及增删节点</th></tr></thead>
        <tbody></tbody>
      </table>
    </div>
    <div class="panel">
      <h2>事件日志</h2>
      <ul id="eventLog"></ul>
    </div>
  </div>
  <div class="panel" id="heatPanel">
    <h2>访问热力图（256 个哈希桶，当前 epoch）</h2>
    <canvas id="heatmap"></canvas>
  </div>
</div>
<script>
var DATA = __REPORT_JSON__;
var HS = 4294967296; // 2^32
var TAU = Math.PI * 2;

// ---- node colors ----
var nodes = DATA.nodes.slice();
var nodeColor = {};
nodes.forEach(function(n, i) {
  nodeColor[n] = "hsl(" + Math.round(i * 360 / Math.max(nodes.length, 1)) + ", 62%, 56%)";
});

// ---- ring geometry ----
var CX = 310, CY = 310, R = 250, RHEAT = 272, RBADGE = 150;
function angleOf(h) { return (h / HS) * TAU - Math.PI / 2; }
function polar(r, a) { return [CX + r * Math.cos(a), CY + r * Math.sin(a)]; }
function arcPath(r, a0, a1) {
  var large = (a1 - a0) > Math.PI ? 1 : 0;
  var p0 = polar(r, a0), p1 = polar(r, a1);
  return "M " + p0[0] + " " + p0[1] + " A " + r + " " + r + " 0 " + large + " 1 " + p1[0] + " " + p1[1];
}
function unwrapEnd(start, end) {
  var e = end;
  if (e <= start) e += HS;
  return e;
}

var svg = document.getElementById("ring");
var SVGNS = "http://www.w3.org/2000/svg";
function el(name, attrs) {
  var e = document.createElementNS(SVGNS, name);
  for (var k in attrs) e.setAttribute(k, attrs[k]);
  return e;
}

// defs: arrowhead marker
var defs = el("defs", {});
var marker = el("marker", { id: "arrow", viewBox: "0 0 10 10", refX: "9", refY: "5",
  markerWidth: "7", markerHeight: "7", orient: "auto-start-reverse" });
marker.appendChild(el("path", { d: "M 0 0 L 10 5 L 0 10 z", fill: "#ffd166" }));
defs.appendChild(marker);
svg.appendChild(defs);

// base circle
svg.appendChild(el("circle", { cx: CX, cy: CY, r: R, fill: "none", stroke: "#232c44", "stroke-width": 16 }));

// shard arcs (final topology)
DATA.finalShards.forEach(function(s) {
  var a0 = angleOf(s.start), a1 = angleOf(unwrapEnd(s.start, s.end));
  svg.appendChild(el("path", {
    d: arcPath(R, a0, a1), fill: "none",
    stroke: nodeColor[s.node] || "#888", "stroke-width": 13, "stroke-opacity": 0.9
  }));
});

// heat overlay group (redrawn per epoch)
var heatG = el("g", {});
svg.appendChild(heatG);

// node badges on inner circle
var badgePos = {};
nodes.forEach(function(n, i) {
  var a = (i / nodes.length) * TAU - Math.PI / 2;
  var p = polar(RBADGE, a);
  badgePos[n] = p;
  svg.appendChild(el("circle", { cx: p[0], cy: p[1], r: 17,
    fill: nodeColor[n], stroke: "#0f1420", "stroke-width": 3 }));
  var t = el("text", { x: p[0], y: p[1] + 4, "text-anchor": "middle",
    "font-size": "10", fill: "#0f1420", "font-weight": "bold" });
  t.textContent = n.replace("node-", "n");
  svg.appendChild(t);
});
var centerLabel = el("text", { x: CX, y: CY - 120, "text-anchor": "middle",
  "font-size": "12", fill: "#8a94a8" });
centerLabel.textContent = "节点";
svg.appendChild(centerLabel);

// migration arrows group
var migG = el("g", {});
svg.appendChild(migG);

// legend
var legend = document.getElementById("nodeLegend");
nodes.forEach(function(n) {
  var s = document.createElement("span");
  var sw = document.createElement("i");
  sw.className = "sw"; sw.style.background = nodeColor[n];
  s.appendChild(sw);
  s.appendChild(document.createTextNode(" " + n));
  legend.appendChild(s);
});
var lh = document.createElement("span");
lh.innerHTML = '<i class="sw" style="background:#e5533d"></i> 访问热度（外环）';
legend.appendChild(lh);
var lm = document.createElement("span");
lm.innerHTML = '<i class="sw" style="background:#ffd166"></i> 迁移路径（箭头指向目标节点）';
legend.appendChild(lm);

// ---- per-epoch rendering ----
var slider = document.getElementById("epochSlider");
var epochLabel = document.getElementById("epochLabel");
slider.max = DATA.epochs;
slider.value = DATA.epochs;

function maxHits(epochIdx) {
  var b = DATA.bucketHits[epochIdx], m = 1;
  for (var i = 0; i < b.length; i++) if (b[i] > m) m = b[i];
  return m;
}

function renderEpoch(epoch) {
  var idx = epoch - 1;
  epochLabel.textContent = "epoch " + epoch + " / " + DATA.epochs;

  // heat overlay on ring
  while (heatG.firstChild) heatG.removeChild(heatG.firstChild);
  var hits = DATA.bucketHits[idx], mx = maxHits(idx);
  for (var i = 0; i < hits.length; i++) {
    if (hits[i] === 0) continue;
    var a0 = angleOf(i * HS / hits.length);
    var a1 = angleOf((i + 1) * HS / hits.length);
    heatG.appendChild(el("path", {
      d: arcPath(RHEAT, a0, a1), fill: "none", stroke: "#e5533d",
      "stroke-width": 10, "stroke-opacity": (0.08 + 0.85 * hits[i] / mx).toFixed(3)
    }));
  }

  // migration arrows active at this epoch
  while (migG.firstChild) migG.removeChild(migG.firstChild);
  DATA.migrations.forEach(function(mg) {
    var active = mg.startEpoch <= epoch && (mg.endEpoch === 0 || epoch <= mg.endEpoch);
    if (!active) return;
    var mid = (mg.lo + mg.hi) / 2 % HS;
    var p0 = polar(R, angleOf(mid));
    var p1 = badgePos[mg.to];
    if (!p1) return;
    // quadratic curve bowed toward center
    var mxp = (p0[0] + p1[0]) / 2, myp = (p0[1] + p1[1]) / 2;
    var cxp = CX + (mxp - CX) * 0.55, cyp = CY + (myp - CY) * 0.55;
    migG.appendChild(el("path", {
      d: "M " + p0[0] + " " + p0[1] + " Q " + cxp + " " + cyp + " " + p1[0] + " " + p1[1],
      fill: "none", stroke: "#ffd166", "stroke-width": 2.5,
      "stroke-dasharray": "7 5", "marker-end": "url(#arrow)"
    }));
    // highlight the migrating arc
    var ha0 = angleOf(mg.lo % HS), ha1 = angleOf(mg.hi % HS);
    migG.appendChild(el("path", {
      d: arcPath(R, ha0, ha1), fill: "none", stroke: "#ffd166",
      "stroke-width": 15, "stroke-opacity": 0.45
    }));
  });

  // heatmap bar chart
  drawHeatmap(idx);

  // event log up to this epoch
  renderEvents(epoch);
}

// ---- heatmap canvas ----
var heatCanvas = document.getElementById("heatmap");
function drawHeatmap(idx) {
  var dpr = window.devicePixelRatio || 1;
  var w = heatCanvas.clientWidth, h = heatCanvas.clientHeight;
  heatCanvas.width = w * dpr; heatCanvas.height = h * dpr;
  var ctx = heatCanvas.getContext("2d");
  ctx.scale(dpr, dpr);
  ctx.clearRect(0, 0, w, h);
  var hits = DATA.bucketHits[idx], mx = maxHits(idx);
  var bw = w / hits.length;
  for (var i = 0; i < hits.length; i++) {
    var v = hits[i] / mx;
    var bh = Math.max(1, v * (h - 14));
    // blue -> orange -> red scale
    var hue = 215 - 215 * v;
    ctx.fillStyle = "hsl(" + Math.round(hue) + ", 75%, " + Math.round(45 + 15 * v) + "%)";
    ctx.fillRect(i * bw, h - bh, Math.max(1, bw - 0.5), bh);
  }
  ctx.fillStyle = "#8a94a8";
  ctx.font = "11px sans-serif";
  ctx.fillText("哈希空间 0", 4, 12);
  var lbl = "2^32";
  ctx.fillText(lbl, w - ctx.measureText(lbl).width - 4, 12);
}

// ---- events ----
var eventLog = document.getElementById("eventLog");
function renderEvents(epoch) {
  eventLog.innerHTML = "";
  DATA.events.forEach(function(ev) {
    if (ev.epoch > epoch) return;
    var li = document.createElement("li");
    var ep = document.createElement("span");
    ep.className = "ep"; ep.textContent = "epoch " + ev.epoch;
    var tag = document.createElement("span");
    tag.className = "tag " + ev.type;
    tag.textContent = { split_start: "拆分", split_done: "完成", node_add: "加节点", node_remove: "删节点" }[ev.type] || ev.type;
    li.appendChild(ep); li.appendChild(tag);
    li.appendChild(document.createTextNode(ev.text));
    eventLog.appendChild(li);
  });
  eventLog.scrollTop = eventLog.scrollHeight;
}

// ---- summary & ratio table ----
var summary = document.getElementById("summary");
[
  ["节点数", DATA.nodes.length],
  ["最终分片数", DATA.finalShards.length],
  ["总请求数", DATA.totalRequests],
  ["迁移次数", DATA.migrations.length],
  ["热点阈值", DATA.hotFactor + "x 均值且 ≥ " + DATA.hotMinHits],
  ["路由一致性错误", DATA.consistencyErrors]
].forEach(function(kv) {
  var d = document.createElement("div");
  var b = document.createElement("b"); b.textContent = kv[1];
  d.appendChild(b);
  d.appendChild(document.createTextNode(kv[0]));
  summary.appendChild(d);
});

var ratioBody = document.querySelector("#ratioTable tbody");
DATA.ratioChecks.forEach(function(rc) {
  var tr = document.createElement("tr");
  [rc.label, rc.moved + " / " + rc.total,
   (rc.measured * 100).toFixed(2) + "%", (rc.expected * 100).toFixed(2) + "%",
   rc.onlyAffected ? "是" : "否"
  ].forEach(function(v) {
    var td = document.createElement("td"); td.textContent = v; tr.appendChild(td);
  });
  ratioBody.appendChild(tr);
});

// ---- playback ----
var playing = false, timer = null;
document.getElementById("playBtn").addEventListener("click", function() {
  playing = !playing;
  this.textContent = playing ? "暂停" : "播放";
  if (playing) {
    if (parseInt(slider.value, 10) >= DATA.epochs) slider.value = 1;
    timer = setInterval(function() {
      var v = parseInt(slider.value, 10);
      if (v >= DATA.epochs) { document.getElementById("playBtn").click(); return; }
      slider.value = v + 1;
      renderEpoch(v + 1);
    }, 650);
  } else if (timer) { clearInterval(timer); timer = null; }
});
slider.addEventListener("input", function() { renderEpoch(parseInt(this.value, 10)); });
window.addEventListener("resize", function() { renderEpoch(parseInt(slider.value, 10)); });

renderEpoch(DATA.epochs);
</script>
</body>
</html>
`
