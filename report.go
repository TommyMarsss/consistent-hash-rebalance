package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// writeReportHTML renders rep as a single self-contained static HTML
// file: all data embedded, no server, no third-party libraries.
func writeReportHTML(rep *Report, path string) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(true) // escapes < > & so the JSON can't break out of <script>
	if err := enc.Encode(rep); err != nil {
		return err
	}
	page := fmt.Sprintf(reportTemplate, buf.String())
	return os.WriteFile(path, []byte(page), 0o644)
}

const reportTemplate = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>一致性哈希热点再平衡报告</title>
<style>
  :root {
    --bg: #0f1420; --panel: #171e2e; --line: #2a3550;
    --text: #dbe4f5; --dim: #8a97b5; --accent: #ffb454; --hot: #ff5a5a;
  }
  * { box-sizing: border-box; }
  body { margin: 0; background: var(--bg); color: var(--text);
         font: 14px/1.6 -apple-system, "PingFang SC", "Helvetica Neue", sans-serif; }
  header { padding: 20px 28px 8px; }
  h1 { font-size: 20px; margin: 0 0 4px; }
  h2 { font-size: 15px; margin: 0 0 10px; color: var(--dim); font-weight: 600; }
  .sub { color: var(--dim); font-size: 12px; }
  main { display: grid; grid-template-columns: minmax(340px, 1fr) minmax(340px, 1fr);
         gap: 16px; padding: 16px 28px 28px; }
  @media (max-width: 900px) { main { grid-template-columns: 1fr; } }
  .card { background: var(--panel); border: 1px solid var(--line); border-radius: 10px;
          padding: 14px 16px; }
  .wide { grid-column: 1 / -1; }
  canvas { display: block; width: 100%%; }
  table { border-collapse: collapse; width: 100%%; font-size: 13px; }
  th, td { text-align: left; padding: 5px 8px; border-bottom: 1px solid var(--line); }
  th { color: var(--dim); font-weight: 600; }
  .ok { color: #6fe3a5; } .bad { color: var(--hot); }
  .event { border-left: 3px solid var(--accent); padding: 6px 10px; margin: 8px 0;
           background: rgba(255,180,84,.06); border-radius: 0 6px 6px 0; cursor: pointer; }
  .event.sel { background: rgba(255,180,84,.18); }
  .event small { color: var(--dim); }
  .legend span { display: inline-block; margin-right: 14px; font-size: 12px; color: var(--dim); }
  .sw { display: inline-block; width: 10px; height: 10px; border-radius: 2px;
        margin-right: 4px; vertical-align: -1px; }
  #tooltip { position: fixed; pointer-events: none; background: #0a0e18; border: 1px solid var(--line);
             border-radius: 6px; padding: 6px 9px; font-size: 12px; display: none; z-index: 9; }
</style>
</head>
<body>
<header>
  <h1>一致性哈希 · 热点感知再平衡报告</h1>
  <div class="sub" id="meta"></div>
</header>
<main>
  <div class="card">
    <h2>哈希环：节点 / 虚拟节点 / 分片分布</h2>
    <canvas id="ring" height="420"></canvas>
    <div class="legend" id="nodeLegend"></div>
  </div>
  <div class="card">
    <h2>分片访问热力图（累计请求数）</h2>
    <canvas id="heat" height="420"></canvas>
    <div class="legend">
      <span><span class="sw" style="background:#3ec1d3"></span>正常分片</span>
      <span><span class="sw" style="background:#ff5a5a"></span>热点拆分产生的分片</span>
      <span>柱高 = 累计访问量；点击柱子可在环上定位</span>
    </div>
  </div>
  <div class="card">
    <h2>再平衡事件（点击在环上查看迁移路径）</h2>
    <div id="events"></div>
  </div>
  <div class="card">
    <h2>节点增删迁移比例校验（理论 ≈ 1/N）</h2>
    <table id="checks">
      <thead><tr><th>事件</th><th>理论比例</th><th>实际比例</th><th>迁移 key 数</th><th>无关迁移</th><th>结论</th></tr></thead>
      <tbody></tbody>
    </table>
  </div>
</main>
<div id="tooltip"></div>
<script>
"use strict";
const REPORT = %s;

const TWO32 = 4294967296;
const $ = id => document.getElementById(id);
const tip = $("tooltip");

// ---- palette per node ------------------------------------------------
const palette = ["#4cc9f0","#f72585","#90be6d","#f9c74f","#b5179e","#43aa8b","#f3722c","#577590","#9d6bce","#2a9d8f"];
const nodeColor = {};
REPORT.nodes.forEach((n, i) => nodeColor[n] = palette[i %% palette.length]);

$("meta").textContent =
  "生成时间 " + REPORT.generatedAt + " · " + REPORT.nodes.length + " 节点 · " +
  REPORT.vnodes.length + " 虚拟节点 · 初始 " + REPORT.initialShards + " 分片 → 当前 " +
  REPORT.shards.length + " 分片 · " + REPORT.epochs + " 个 epoch / " +
  REPORT.requests.toLocaleString() + " 请求 · 热点阈值 " + REPORT.detectorMult + "× 均值且 ≥ " + REPORT.detectorMin;

// ---- shared geometry -------------------------------------------------
function angleOf(h) { return (h / TWO32) * Math.PI * 2 - Math.PI / 2; }
function nodeAngle(node) {
  const vs = REPORT.vnodes.filter(v => v.node === node);
  if (!vs.length) return 0;
  // circular mean of vnode angles
  let x = 0, y = 0;
  vs.forEach(v => { const a = angleOf(v.hash); x += Math.cos(a); y += Math.sin(a); });
  return Math.atan2(y, x);
}

let selectedEvent = -1, selectedShard = null;

// ---- ring canvas -----------------------------------------------------
function drawRing() {
  const cv = $("ring"), dpr = window.devicePixelRatio || 1;
  const W = cv.clientWidth, H = 420;
  cv.width = W * dpr; cv.height = H * dpr;
  const g = cv.getContext("2d"); g.scale(dpr, dpr);
  const cx = W / 2, cy = H / 2, R = Math.min(W, H) / 2 - 46;

  g.clearRect(0, 0, W, H);
  g.strokeStyle = "#2a3550"; g.lineWidth = 1;
  g.beginPath(); g.arc(cx, cy, R, 0, Math.PI * 2); g.stroke();

  // shard arcs (outer band), colored by owner; hot-born shards outlined
  const maxHeat = Math.max(1, ...REPORT.shards.map(s => s.heat));
  REPORT.shards.forEach(s => {
    const a0 = angleOf(s.start), a1 = s.end === 0 ? Math.PI * 1.5 : angleOf(s.end);
    g.beginPath();
    g.arc(cx, cy, R + 14, a0, a1);
    g.strokeStyle = nodeColor[s.node] || "#888";
    g.globalAlpha = 0.35 + 0.65 * (s.heat / maxHeat);
    g.lineWidth = s.hot ? 12 : 8;
    g.stroke();
    g.globalAlpha = 1;
    if (s.hot) { // hot marker
      const mid = (a0 + a1) / 2;
      g.fillStyle = "#ff5a5a";
      g.beginPath(); g.arc(cx + Math.cos(mid) * (R + 30), cy + Math.sin(mid) * (R + 30), 4, 0, Math.PI * 2); g.fill();
    }
  });

  // vnodes (inner ticks)
  REPORT.vnodes.forEach(v => {
    const a = angleOf(v.hash);
    g.strokeStyle = nodeColor[v.node] || "#888";
    g.lineWidth = 1.5;
    g.beginPath();
    g.moveTo(cx + Math.cos(a) * (R - 10), cy + Math.sin(a) * (R - 10));
    g.lineTo(cx + Math.cos(a) * (R + 2), cy + Math.sin(a) * (R + 2));
    g.stroke();
  });

  // physical nodes
  REPORT.nodes.forEach(n => {
    const a = nodeAngle(n);
    const x = cx + Math.cos(a) * (R - 26), y = cy + Math.sin(a) * (R - 26);
    g.fillStyle = nodeColor[n];
    g.beginPath(); g.arc(x, y, 7, 0, Math.PI * 2); g.fill();
    g.fillStyle = "#dbe4f5"; g.font = "11px sans-serif"; g.textAlign = "center";
    g.fillText(n.replace("node-", ""), x, y - 12);
  });

  // migration path of the selected event
  if (selectedEvent >= 0) {
    const ev = REPORT.events[selectedEvent];
    const aFrom = nodeAngle(ev.oldNode);
    ev.targets.forEach((t, i) => {
      if (t === ev.oldNode) return;
      const aTo = nodeAngle(t);
      const x0 = cx + Math.cos(aFrom) * (R - 40), y0 = cy + Math.sin(aFrom) * (R - 40);
      const x1 = cx + Math.cos(aTo) * (R - 40), y1 = cy + Math.sin(aTo) * (R - 40);
      g.strokeStyle = "#ffb454"; g.lineWidth = 2; g.setLineDash([6, 4]);
      g.beginPath(); g.moveTo(x0, y0);
      g.quadraticCurveTo(cx, cy, x1, y1); g.stroke();
      g.setLineDash([]);
      // arrowhead
      const ang = Math.atan2(y1 - cy, x1 - cx);
      g.fillStyle = "#ffb454";
      g.beginPath();
      g.moveTo(x1, y1);
      g.lineTo(x1 - 10 * Math.cos(ang - 0.4), y1 - 10 * Math.sin(ang - 0.4));
      g.lineTo(x1 - 10 * Math.cos(ang + 0.4), y1 - 10 * Math.sin(ang + 0.4));
      g.closePath(); g.fill();
      const sh = ev.newShards[i];
      g.fillStyle = "#ffb454"; g.font = "10px sans-serif";
      g.fillText(sh, (x0 + x1) / 2, (y0 + y1) / 2 - 6);
    });
  }
  // selected shard locator
  if (selectedShard) {
    const s = REPORT.shards.find(x => x.id === selectedShard);
    if (s) {
      const a0 = angleOf(s.start), a1 = s.end === 0 ? Math.PI * 1.5 : angleOf(s.end);
      g.strokeStyle = "#ffffff"; g.lineWidth = 2;
      g.beginPath(); g.arc(cx, cy, R + 22, a0, a1); g.stroke();
    }
  }
}

// ---- heat canvas -----------------------------------------------------
let heatBars = [];
function drawHeat() {
  const cv = $("heat"), dpr = window.devicePixelRatio || 1;
  const W = cv.clientWidth, H = 420;
  cv.width = W * dpr; cv.height = H * dpr;
  const g = cv.getContext("2d"); g.scale(dpr, dpr);
  g.clearRect(0, 0, W, H);
  const maxHeat = Math.max(1, ...REPORT.shards.map(s => s.heat));
  const n = REPORT.shards.length, bw = W / n;
  heatBars = [];
  REPORT.shards.forEach((s, i) => {
    const h = (s.heat / maxHeat) * (H - 60);
    const x = i * bw, y = H - 30 - h;
    g.fillStyle = s.hot ? "#ff5a5a" : "#3ec1d3";
    if (s.id === selectedShard) g.fillStyle = "#ffffff";
    g.fillRect(x + 0.5, y, Math.max(1, bw - 1.5), h);
    heatBars.push({ x, w: bw, id: s.id, heat: s.heat, node: s.node, y });
  });
  g.fillStyle = "#8a97b5"; g.font = "11px sans-serif"; g.textAlign = "left";
  g.fillText("分片（按哈希区间排序）→", 6, H - 10);
  g.fillText("max " + maxHeat.toLocaleString(), 6, 14);
}

$("heat").addEventListener("mousemove", e => {
  const r = e.target.getBoundingClientRect();
  const mx = e.clientX - r.left, my = e.clientY - r.top;
  const b = heatBars.find(b => mx >= b.x && mx < b.x + b.w);
  if (b) {
    tip.style.display = "block";
    tip.style.left = (e.clientX + 12) + "px"; tip.style.top = (e.clientY + 12) + "px";
    tip.textContent = b.id + " · " + b.node + " · " + b.heat.toLocaleString() + " 次";
  } else tip.style.display = "none";
});
$("heat").addEventListener("mouseleave", () => tip.style.display = "none");
$("heat").addEventListener("click", e => {
  const r = e.target.getBoundingClientRect();
  const mx = e.clientX - r.left;
  const b = heatBars.find(b => mx >= b.x && mx < b.x + b.w);
  selectedShard = b && b.id !== selectedShard ? b.id : null;
  drawRing(); drawHeat();
});

// ---- events list -----------------------------------------------------
const evBox = $("events");
if (!REPORT.events.length) evBox.innerHTML = "<p class='sub'>本次运行未触发热点拆分。</p>";
REPORT.events.forEach((ev, i) => {
  const div = document.createElement("div");
  div.className = "event";
  div.innerHTML = "<b>#" + ev.seq + "</b> epoch " + ev.epoch + " · " + ev.reason +
    "<br><small>" + ev.oldShard + "（" + ev.oldNode + "）→ " +
    ev.newShards.map((s, j) => s + "→" + ev.targets[j]).join("，") +
    " · 迁移 " + ev.keysMoved.toLocaleString() + " key</small>";
  div.onclick = () => {
    selectedEvent = selectedEvent === i ? -1 : i;
    document.querySelectorAll(".event").forEach((el, j) => el.classList.toggle("sel", j === selectedEvent));
    drawRing();
  };
  evBox.appendChild(div);
});

// ---- ratio checks table ----------------------------------------------
const tbody = document.querySelector("#checks tbody");
REPORT.ratioChecks.forEach(c => {
  const ok = c.unrelatedMoves === 0 && Math.abs(c.actual - c.expected) < c.expected * 0.6 + 0.01;
  const tr = document.createElement("tr");
  tr.innerHTML = "<td>" + c.event + "</td><td>" + (c.expected * 100).toFixed(2) + "%%</td>" +
    "<td>" + (c.actual * 100).toFixed(2) + "%%</td>" +
    "<td>" + c.movedKeys.toLocaleString() + " / " + c.totalKeys.toLocaleString() + "</td>" +
    "<td>" + c.unrelatedMoves + "</td>" +
    "<td class='" + (ok ? "ok" : "bad") + "'>" + (ok ? "✓ 符合预期" : "✗ 异常") + "</td>";
  tbody.appendChild(tr);
});

// ---- node legend -------------------------------------------------------
$("nodeLegend").innerHTML = REPORT.nodes.map(n =>
  "<span><span class='sw' style='background:" + nodeColor[n] + "'></span>" + n + "</span>").join("");

window.addEventListener("resize", () => { drawRing(); drawHeat(); });
drawRing(); drawHeat();
</script>
</body>
</html>
`
