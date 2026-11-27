'use strict';

const PHASES = ['off','post','pxe','tftp','http','install','reboot','done','fault'];
const PHASE_COLOR = {
  off:'#3a4353', post:'#8b98a9', pxe:'#4f9dff', tftp:'#39c5cf', http:'#bc8cff',
  install:'#d29922', reboot:'#f0883e', done:'#3fb950', fault:'#f85149',
};
const HELD_COLOR = '#6b5324';
const PHASE_LABEL = {
  off:'未上电/拦截', post:'固件自检', pxe:'DHCP 获取地址', tftp:'下载引导程序',
  http:'拉取安装镜像', install:'分区装机', reboot:'重启进新系统', done:'装机完成', fault:'故障停止',
};

let META = null;
let OVERVIEW = null;
let state = { kind: 'rhythm', wall: null, scrubT: null };

const $ = (sel) => document.querySelector(sel);
const el = (tag, cls, txt) => {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (txt !== undefined) e.textContent = txt;
  return e;
};

async function getJSON(url) {
  const r = await fetch(url);
  if (!r.ok) throw new Error(url + ' ' + r.status);
  return r.json();
}

// ---------------------------------------------------------------------------
// Boot
// ---------------------------------------------------------------------------
async function boot() {
  [META, OVERVIEW] = await Promise.all([getJSON('/api/meta'), getJSON('/api/overview')]);
  const q0 = new URLSearchParams(location.search);
  if (q0.has('s')) state.kind = q0.get('s');
  $('#subtitle').textContent =
    `${META.total} 台 · ${META.cabinets} 个机柜 · ${META.models.length} 种机型 · 种子 ${META.seed.toString(16)}`;
  renderStrategies();
  await selectStrategy(state.kind);
}

function renderStrategies() {
  const nav = $('#strategies');
  nav.innerHTML = '';
  META.strategies.forEach((s, i) => {
    if (s.kind === 'naive') state.kind = state.kind || s.kind;
    const b = el('button', '', s.label);
    b.dataset.kind = s.kind;
    if (s.kind === state.kind) b.classList.add('on');
    b.onClick = null;
    b.onclick = () => selectStrategy(s.kind);
    nav.appendChild(b);
  });
}

async function selectStrategy(kind) {
  state.kind = kind;
  document.querySelectorAll('#strategies button').forEach(b =>
    b.classList.toggle('on', b.dataset.kind === kind));
  state.scrubT = null;
  const [wall, summary, svc, logs, machines] = await Promise.all([
    getJSON(`/api/run/${kind}/wall`),
    getJSON(`/api/run/${kind}/summary`),
    getJSON(`/api/run/${kind}/services`),
    getJSON(`/api/run/${kind}/logs`),
    getJSON(`/api/run/${kind}/machines`),
  ]);
  state.wall = wall;
  state.machines = machines;
  renderKPIs(summary, wall);
  renderWall();
  renderPhaseChart(summary);
  renderBottlenecks();
  renderRhythm();
  renderCompare(machines);
  renderScorecard(summary);
  renderCabinets(wall);
  renderFaultClasses(summary);
  renderTriage(summary);
  renderSwitches();
  renderAudit();
  renderTicker(logs);
}

// ---------------------------------------------------------------------------
// KPIs
// ---------------------------------------------------------------------------
function renderKPIs(s, wall) {
  const k = $('#kpis');
  k.innerHTML = '';
  const total = META.total;
  const add = (cls, v, key, sub) => {
    const c = el('div', 'kpi ' + cls);
    c.appendChild(el('div', 'v', v));
    c.appendChild(el('div', 'k', key));
    if (sub) c.appendChild(el('div', 'sub', sub));
    k.appendChild(c);
  };
  add('acc', s.makespanH.toFixed(1) + ' h', '总耗时', `最后一批完成`);
  add('good', s.installed - s.noncompliant, '合规装好', `共 ${total} 台`);
  add('bad', s.failed, '故障停止', `卡在某一阶段`);
  add('warn', s.held, '预检拦截', `未上电`);
  const capLoss = s.capacityLost || 0;
  add(capLoss ? 'bad' : 'good', capLoss, '资源不足失败', capLoss ? '排队超时/地址耗尽' : '无');
  if (s.score) {
    add('acc', (s.score.exactRate * 100).toFixed(0) + '%', '归因精确率', `${s.score.exact}/${s.score.totalDefects} 个真问题`);
  }
}

// ---------------------------------------------------------------------------
// Phase wall
// ---------------------------------------------------------------------------
function phaseAt(tr, t) {
  if (!tr || !tr.length) return 'off';
  if (t < tr[0][0]) return 'off';
  let ph = 'off';
  for (const e of tr) { if (e[0] > t) break; ph = e[1]; }
  return ph;
}

function currentPhase(h) {
  if (state.scrubT === null) return h.phase;
  // JSON object keys are strings; the host ids are numbers.
  return phaseAt(state.wall.transitions[String(h.id)], state.scrubT);
}

function renderWall() {
  const wall = $('#wall');
  wall.innerHTML = '';
  const maxT = Math.max(state.wall.makespan, 1);
  if (state.scrubT === null) state.scrubT = maxT;
  const scrub = $('#scrub');
  scrub.max = 1000;
  scrub.value = Math.round((state.scrubT / maxT) * 1000);
  scrub.oninput = () => {
    state.scrubT = (Number(scrub.value) / 1000) * maxT;
    $('#scrub-t').textContent = (state.scrubT / 60).toFixed(0) + ' min';
    renderWallCells();
    renderPhaseChartLive();
  };
  $('#scrub-t').textContent = (state.scrubT / 60).toFixed(0) + ' min';

  // One column per cabinet, cells stacked down the column (U1→U40).
  const per = META.perCabinet;
  for (let u = 0; u < per; u++) {
    for (let cid = 1; cid <= META.cabinets; cid++) {
      const idx = (cid - 1) * per + u;
      const c = el('div', 'cell');
      c.dataset.id = state.wall.hosts[idx].id;
      wall.appendChild(c);
    }
  }
  renderWallCells();
}

function renderWallCells() {
  const hosts = state.wall.hosts;
  const per = META.perCabinet, nc = META.cabinets;
  const cells = document.querySelectorAll('#wall .cell');
  const cnt = {};
  hosts.forEach((h) => { const ph = currentPhase(h); cnt[ph] = (cnt[ph] || 0) + 1; });
  // index: cell j corresponds to U-row j/nc, cabinet (j%nc)+1
  for (let j = 0; j < cells.length; j++) {
    const u = Math.floor(j / nc);
    const cid = (j % nc) + 1;
    const idx = (cid - 1) * per + u;
    const h = hosts[idx];
    if (!h) continue;
    const ph = currentPhase(h);
    const cell = cells[j];
    cell.style.background = h.hold ? HELD_COLOR : (PHASE_COLOR[ph] || PHASE_COLOR.off);
    cell.classList.toggle('held', h.hold && ph === 'off');
    cell.title = `${h.hostname}  ${PHASE_LABEL[ph]}` + (h.hold ? ` (拦截: ${h.auditLabel || ''})` : '');
    cell.onclick = () => openMachine(h.id);
  }
  renderPhaseLegend(cnt);
}

function renderPhaseLegend(cnt) {
  const lg = $('#phase-legend');
  lg.innerHTML = '';
  PHASES.forEach(p => {
    const n = cnt[p] || 0;
    if (!n) return;
    const s = el('span');
    const i = el('i'); i.style.background = PHASE_COLOR[p];
    s.appendChild(i);
    s.appendChild(document.createTextNode(`${PHASE_LABEL[p]} ${n}`));
    lg.appendChild(s);
  });
}

// ---------------------------------------------------------------------------
// Phase chart
// ---------------------------------------------------------------------------
function renderPhaseChartLive() {
  const cnt = {};
  state.wall.hosts.forEach(h => { const ph = currentPhase(h); cnt[ph] = (cnt[ph]||0)+1; });
  const c = $('#phase-chart');
  c.innerHTML = '';
  const max = Math.max(...Object.values(cnt), 1);
  PHASES.forEach(p => {
    const n = cnt[p] || 0;
    const row = el('div', 'bar-row');
    row.appendChild(el('div', '', PHASE_LABEL[p]));
    const track = el('div', 'bar-track');
    const fill = el('div', 'bar-fill');
    fill.style.width = (n / max * 100) + '%';
    fill.style.background = PHASE_COLOR[p];
    track.appendChild(fill);
    row.appendChild(track);
    row.appendChild(el('div', 'bar-num', String(n)));
    c.appendChild(row);
  });
}

function renderPhaseChart(s) {
  const c = $('#phase-chart');
  c.innerHTML = '';
  const max = Math.max(...s.phases.map(p => p.count), 1);
  s.phases.forEach(p => {
    const row = el('div', 'bar-row');
    row.appendChild(el('div', '', p.label));
    const track = el('div', 'bar-track');
    const fill = el('div', 'bar-fill');
    fill.style.width = (p.count / max * 100) + '%';
    fill.style.background = PHASE_COLOR[p.phase] || PHASE_COLOR.off;
    track.appendChild(fill);
    row.appendChild(track);
    row.appendChild(el('div', 'bar-num', String(p.count)));
    c.appendChild(row);
  });
}

// ---------------------------------------------------------------------------
// Bottlenecks
// ---------------------------------------------------------------------------
function renderBottlenecks() {
  const box = $('#bottlenecks');
  box.innerHTML = '';
  META.bottlenecks.forEach(b => {
    const hot = b.capacity > 0 && b.peakInUse >= b.capacity;
    const row = el('div', 'bn' + (hot ? ' hot' : ''));
    row.appendChild(el('div', 'rank', String(b.rank)));
    const body = el('div', 'body');
    body.appendChild(el('h3', '', b.label));
    const meta = el('div', 'meta');
    meta.textContent = `容量 ${b.capacity} · 峰值占用 ${b.peakInUse} · 排队峰值 ${b.peakQueue}` +
      (b.firstFullS >= 0 ? ` · 首次满 @ ${Math.round(b.firstFullS)}s` : '');
    body.appendChild(meta);
    const track = el('div', 'mini-track');
    const fill = el('div', 'mini-fill' + (hot ? '' : ' ok'));
    fill.style.width = Math.min(100, b.capacity ? b.peakInUse / b.capacity * 100 : 0) + '%';
    track.appendChild(fill);
    body.appendChild(track);
    body.appendChild(el('div', 'coach', b.coaching));
    row.appendChild(body);
    box.appendChild(row);
  });
}

// ---------------------------------------------------------------------------
// Rhythm
// ---------------------------------------------------------------------------
function renderRhythm() {
  const box = $('#rhythm');
  box.innerHTML = '';
  const r = META.rhythm;
  const head = el('div', 'rhythm-head');
  head.innerHTML = `每批 <b>${r.perWave}</b> 台 · 批间隔 <b>${Math.round(r.waveGapS)}</b> 秒 · 共 <b>${r.waves.length}</b> 批 · 预计 <b>${(r.estMakespanS / 3600).toFixed(1)}</b> h`;
  box.appendChild(head);

  r.basis.forEach(l => {
    const p = el('div', 'rhythm-head');
    p.style.fontSize = '12.5px';
    p.style.color = 'var(--muted)';
    p.textContent = '· ' + l;
    box.appendChild(p);
  });

  const strip = el('div', 'wave-strip');
  r.waves.forEach((w, i) => {
    const chip = el('div', 'wave-chip', `#${i + 1} ${w.count}台 @${Math.round(w.atS / 60)}m`);
    strip.appendChild(chip);
  });
  box.appendChild(strip);

  const tbl = el('table', 'probe-table');
  tbl.innerHTML = '<thead><tr><th>每批台数</th><th>批数</th><th>用时(h)</th><th>资源失败</th><th>最大排队</th></tr></thead>';
  const tb = el('tbody');
  META.probe.forEach(p => {
    const tr = el('tr', p.perWave === r.perWave ? 'pick' : '');
    tr.innerHTML = `<td>${p.perWave}</td><td>${p.waves}</td><td>${(p.makespanS / 3600).toFixed(1)}</td><td>${p.capacityLost}</td><td>${p.maxQueue}</td>`;
    tb.appendChild(tr);
  });
  tbl.appendChild(tb);
  box.appendChild(tbl);
}

// ---------------------------------------------------------------------------
// Comparison
// ---------------------------------------------------------------------------
function renderCompare(machines) {
  const box = $('#compare');
  box.innerHTML = '';
  const tbl = el('table', 'cmp');
  tbl.innerHTML = `<thead><tr>
    <th>方案</th><th>用时h</th><th>合规装好</th><th>拦截</th><th>故障</th><th>资源失败</th><th>归因准确</th><th>排查工分</th></tr></thead>`;
  const tb = el('tbody');
  OVERVIEW.comparison.rows.forEach(r => {
    const tr = el('tr', r.kind === state.kind ? 'on' : 'clickable');
    tr.innerHTML = `
      <td>${r.label}</td>
      <td class="${r.makespanH <= 8 ? 'good' : ''}">${r.makespanH.toFixed(1)}</td>
      <td class="good">${r.compliant}</td>
      <td>${r.held}</td>
      <td class="${r.failed > 20 ? 'bad' : ''}">${r.failed}</td>
      <td class="${r.capacityLost > 0 ? 'bad' : 'good'}">${r.capacityLost}</td>
      <td>${(r.exactRate * 100).toFixed(0)}%</td>
      <td class="${r.triage.effortMin > 150 ? 'bad' : r.triage.effortMin < 100 ? 'good' : ''}">${r.triage.effortMin.toFixed(0)}</td>`;
    tr.onclick = () => selectStrategy(r.kind);
    tb.appendChild(tr);
  });
  tbl.appendChild(tb);
  box.appendChild(tbl);
}

// ---------------------------------------------------------------------------
// Scorecard
// ---------------------------------------------------------------------------
function renderScorecard(s) {
  const box = $('#scorecard');
  box.innerHTML = '';
  const sc = s.score;
  if (!sc) { box.appendChild(el('div', 'empty', '无真值')); return; }

  const top = el('div', 'score-top');
  const big = el('div', 'score-big', (sc.exactRate * 100).toFixed(0) + '%');
  big.appendChild(el('small', '', '精确归因'));
  top.appendChild(big);
  const b2 = el('div', 'score-big', (sc.recall * 100).toFixed(0) + '%');
  b2.style.color = 'var(--accent)';
  b2.appendChild(el('small', '', '召回(精确+近似)'));
  top.appendChild(b2);
  box.appendChild(top);

  const grid = el('div', 'match-grid');
  const cell = (n, l, col) => {
    const c = el('div', 'match-cell');
    const v = el('div', 'n', String(n)); v.style.color = col;
    c.appendChild(v); c.appendChild(el('div', 'l', l));
    return c;
  };
  grid.appendChild(cell(sc.exact, '精确', 'var(--good)'));
  grid.appendChild(cell(sc.symptom, '近似', 'var(--accent)'));
  grid.appendChild(cell(sc.miss, '错判', 'var(--warn)'));
  grid.appendChild(cell(sc.unresolved, '未判定', 'var(--muted)'));
  grid.appendChild(cell(sc.falsePositive, '误报', 'var(--bad)'));
  box.appendChild(grid);

  // Confusion: only non-exact pairs are interesting.
  const list = el('div', 'conf-list');
  const bad = sc.pairs.filter(p => p.match !== 'exact' && p.verdict !== 'ok');
  if (!bad.length) list.appendChild(el('div', 'empty', '真值与工具归因完全一致'));
  bad.slice(0, 200).forEach(p => {
    const row = el('div', 'conf-row');
    const t = el('div', 't', `#${p.id} ${p.hostname}`); t.style.flex = '0 0 132px';
    row.appendChild(t);
    row.appendChild(el('div', 't', p.truth));
    row.appendChild(el('div', 'a', '→ ' + p.answer));
    row.onclick = () => openMachine(p.id);
    row.style.cursor = 'pointer';
    list.appendChild(row);
  });
  box.appendChild(list);
}

// ---------------------------------------------------------------------------
// Cabinets
// ---------------------------------------------------------------------------
function renderCabinets(wall) {
  const box = $('#cabinets');
  box.innerHTML = '';
  const cabs = {};
  wall.hosts.forEach(h => {
    if (!cabs[h.cabinet]) cabs[h.cabinet] = [];
    cabs[h.cabinet].push(h);
  });
  Object.keys(cabs).sort((a, b) => Number(a) - Number(b)).forEach(id => {
    const hosts = cabs[id];
    const total = hosts.length;
    const done = hosts.filter(h => h.phase === 'done').length;
    const held = hosts.filter(h => h.hold).length;
    const failed = hosts.filter(h => h.phase === 'fault').length;
    // Dominant fault
    const fc = {};
    hosts.forEach(h => {
      const code = h.fault || (h.hold ? h.auditCode : null);
      if (code) fc[code] = (fc[code] || 0) + 1;
    });
    let topF = null, topN = 0;
    Object.entries(fc).forEach(([f, n]) => { if (n > topN) { topN = n; topF = f; } });
    const cluster = topF && topN * 10 >= total * 3;

    const c = el('div', 'cab' + (cluster ? ' cluster' : ''));
    const head = el('div', 'cab-head');
    head.appendChild(el('b', '', 'CAB-' + String(id).padStart(2, '0')));
    head.appendChild(el('span', '', `${done}/${total} 好`));
    c.appendChild(head);

    const bar = el('div', 'cab-bar');
    const seg = (n, col) => {
      if (!n) return;
      const i = el('i');
      i.style.width = (n / total * 100) + '%';
      i.style.background = col;
      bar.appendChild(i);
    };
    seg(done, '#3fb950'); seg(failed, '#f85149'); seg(held, '#5a4a2a');
    c.appendChild(bar);

    const lab = el('div', 'cab-fault');
    if (topF) lab.textContent = `${labelForFault(topF)} ×${topN}`;
    else if (held) lab.textContent = `拦截 ${held} 台`;
    c.appendChild(lab);
    c.onclick = () => openCabinet(id);
    box.appendChild(c);
  });
}

function labelForFault(code) {
  const m = META.bottlenecks; // no fault labels there; use a lookup
  return FAULT_LABEL[code] || code;
}
const FAULT_LABEL = {
  'boot.order':'启动顺序错误', 'boot.usbstick':'启动顺序错误', 'boot.silent':'无网络启动请求',
  'firmware.pxedisabled':'网卡PXE禁用', 'firmware.raidmode':'控制器AHCI模式',
  'template.mismatch':'模板不适用', 'install.raidcfg':'阵列配置不符',
  'hw.dimm.count':'内存条数不符', 'hw.dimm.model':'内存型号不符',
  'hw.disk.model':'硬盘型号不符', 'hw.disk.count':'硬盘数量不符',
  'hw.nic.firmware':'网卡固件过低', 'hw.nic.model':'网卡型号不符', 'hw.disk.smart':'硬盘SMART故障',
  'hw.mem.ecc':'内存ECC错误', 'net.switch.off':'机柜交换机未放行', 'net.switch.cap':'交换机会话已满',
  'dhcp.exhausted':'地址池耗尽', 'dhcp.timeout':'地址请求超时',
  'tftp.capacity':'引导服务排队超时', 'tftp.corrupt':'引导程序校验失败',
  'http.capacity':'镜像服务排队超时', 'http.timeout':'镜像传输超时', 'image.corrupt':'镜像校验失败',
  'install.fail':'装机脚本失败',
};

// ---------------------------------------------------------------------------
// Triage burden
// ---------------------------------------------------------------------------
function renderTriage(s) {
  const box = $('#triage');
  box.innerHTML = '';
  const t = s.triage;
  if (!t) { box.appendChild(el('div', 'empty', '无数据')); return; }

  const head = el('div', 'rhythm-head');
  head.innerHTML = `共 <b>${t.uniqueSignatures}</b> 类症状 · 共因批处理 <b>${t.batchFixes}</b> 类(覆盖 ${t.batchCovered} 台) · <b>${t.soloCases}</b> 台需单台排查 · 约 <b>${t.effortMin.toFixed(0)}</b> 工程师分钟`;
  box.appendChild(head);

  if (!t.groups || !t.groups.length) {
    box.appendChild(el('div', 'empty', '无意外故障,这批全部可预期处理'));
    return;
  }
  const tbl = el('table', 'probe-table');
  tbl.innerHTML = '<thead><tr><th>症状</th><th>阶段</th><th>台数</th><th>处理</th></tr></thead>';
  const tb = el('tbody');
  t.groups.forEach(g => {
    const tr = el('tr');
    const tag = g.systemic ? '<span class="pill warn">共因</span>' : '<span class="pill">个案</span>';
    tr.innerHTML = `<td>${g.label}</td><td>${PHASE_LABEL[g.phase] || g.phase}</td><td>${g.count}</td><td>${tag}</td>`;
    tb.appendChild(tr);
  });
  tbl.appendChild(tb);
  box.appendChild(tbl);
  const note = el('div', 'coach');
  note.textContent = '共因≥3台同一原因,修一处清一片;个案需逐台开箱或接显示器。';
  note.style.marginTop = '10px';
  box.appendChild(note);
}

// ---------------------------------------------------------------------------
// Switches
// ---------------------------------------------------------------------------
function renderSwitches() {
  const box = $('#switches');
  box.innerHTML = '';
  const sw = Object.values(META.switches).sort((a, b) => a.cabinetId - b.cabinetId);
  const bad = sw.filter(s => !s.bootVlanEnabled || s.maxBootSessions < 40);
  if (!bad.length) {
    box.appendChild(el('div', 'empty', '所有机柜交换机启动网络配置正常'));
    return;
  }
  bad.forEach(s => {
    const row = el('div', 'bn hot');
    row.appendChild(el('div', 'rank', '⚠'));
    const body = el('div', 'body');
    body.appendChild(el('h3', '', `${s.id} · CAB-${String(s.cabinetId).padStart(2,'0')}`));
    const issues = [];
    if (!s.bootVlanEnabled) issues.push(`启动 VLAN ${s.bootVlan} 未放行 → 整柜收不到 DHCP 应答`);
    if (s.maxBootSessions < 40) issues.push(`启动会话上限 ${s.maxBootSessions} < 40 台 → 整柜并发启动被丢弃`);
    body.appendChild(el('div', 'coach', issues.join(' ; ')));
    row.appendChild(body);
    box.appendChild(row);
  });
}

// ---------------------------------------------------------------------------
// Fault classes
// ---------------------------------------------------------------------------
function renderFaultClasses(s) {
  const box = $('#fault-classes');
  box.innerHTML = '';
  if (!s.faults.length) { box.appendChild(el('div', 'empty', '无故障')); return; }
  const max = Math.max(...s.faults.map(f => f.count), 1);
  s.faults.forEach(f => {
    const row = el('div', 'bar-row');
    row.appendChild(el('div', '', f.label));
    const track = el('div', 'bar-track');
    const fill = el('div', 'bar-fill');
    fill.style.width = (f.count / max * 100) + '%';
    fill.style.background = f.category === '网络/服务' ? '#f85149'
      : f.category === '硬件' ? '#d29922' : f.category === '配置/模板' ? '#bc8cff' : '#4f9dff';
    track.appendChild(fill);
    row.appendChild(track);
    row.appendChild(el('div', 'bar-num', String(f.count)));
    row.title = f.category;
    box.appendChild(row);
  });
}

// ---------------------------------------------------------------------------
// Audit summary
// ---------------------------------------------------------------------------
let AUDIT = null;
async function renderAudit() {
  if (!AUDIT) AUDIT = await getJSON('/api/audit');
  const box = $('#audit-summary');
  box.innerHTML = '';
  const sm = AUDIT.summary;
  const head = el('div', 'kv');
  head.innerHTML = `<span>合格</span><span class="good">${sm.ok} 台</span>`;
  box.appendChild(head);
  const head2 = el('div', 'kv');
  head2.innerHTML = `<span>拦截</span><span class="warn">${sm.held} 台</span>`;
  box.appendChild(head2);

  const byRepair = sm.byRepair || {};
  const repairLabel = {physical:'物理处理(开箱换件)', remote:'远程 BMC 可修', switch:'交换机/网络', template:'修模板'};
  Object.entries(byRepair).forEach(([k, v]) => {
    const kv = el('div', 'kv');
    kv.innerHTML = `<span>${repairLabel[k] || k}</span><span>${v} 台</span>`;
    box.appendChild(kv);
  });

  if (sm.switchIssues && sm.switchIssues.length) {
    box.appendChild(el('div', 'section-title', '机柜级问题'));
    sm.switchIssues.forEach(si => {
      const d = el('div', 'issue');
      const desc = el('div', 'desc', si.issue.detail);
      desc.style.color = 'var(--bad)';
      d.appendChild(desc);
      box.appendChild(d);
    });
  }
}

// ---------------------------------------------------------------------------
// Ticker
// ---------------------------------------------------------------------------
function renderTicker(logs) {
  const box = $('#ticker');
  box.innerHTML = '';
  if (!logs.length) { box.appendChild(el('div', 'empty', '无告警')); return; }
  logs.slice(0, 300).forEach(e => {
    const row = el('div', 'tick');
    row.appendChild(el('div', 't', fmtDur(e.t)));
    row.appendChild(el('div', 'sev ' + e.severity, e.severity === 'error' ? '错误' : '告警'));
    row.appendChild(el('div', 'l', `#${e.id} ${e.line}`));
    row.onclick = () => openMachine(e.id);
    row.style.cursor = 'pointer';
    box.appendChild(row);
  });
}

function fmtDur(sec) {
  if (sec < 120) return Math.round(sec) + 's';
  const h = Math.floor(sec / 3600), m = Math.round((sec % 3600) / 60);
  return h ? `${h}h${m}m` : `${m}m`;
}

// ---------------------------------------------------------------------------
// Drawer: machine + cabinet
// ---------------------------------------------------------------------------
function openDrawer(title) {
  $('#drawer-title').textContent = title;
  $('#drawer').classList.remove('hidden');
}
function closeDrawer() { $('#drawer').classList.add('hidden'); }

$('#drawer-close').onclick = closeDrawer;
document.addEventListener('keydown', e => { if (e.key === 'Escape') closeDrawer(); });

async function openMachine(id) {
  const d = await getJSON(`/api/machine/${state.kind}/${id}`);
  openDrawer(`${d.record.hostname} · #${id}`);
  const body = $('#drawer-body');
  body.innerHTML = '';

  const grid = el('div', 'mv-grid');
  grid.appendChild(box('状态', [
    ['当前阶段', PHASE_LABEL[d.record.phase]],
    ['判定', d.record.verdict],
    ['真值(埋入)', d.truthLabel],
    ['工具归因', d.record.attribution ? (FAULT_LABEL[d.record.attribution] || d.record.attribution) : '—'],
    ['核对', d.record.matches],
    ['完成时刻', d.record.doneAtS ? fmtDur(d.record.doneAtS) : '—'],
  ]));
  grid.appendChild(box('硬件 vs 采购单', [
    ['机型/机柜', `${d.record.model} · CAB-${String(d.record.cabinet).padStart(2,'0')} U${d.record.rackU}`],
    ['内存', `${d.inventory.dimms.count}×${d.inventory.dimms.perDimmGB}GB ${d.inventory.dimms.model}`],
    ['应配内存', `${d.procurement.dimms.count}×${d.procurement.dimms.perDimmGB}GB ${d.procurement.dimms.model}`],
    ['硬盘', `${d.inventory.disks.count}×${d.inventory.disks.model} ${d.inventory.disks.sizeGB}GB`],
    ['应配硬盘', `${d.procurement.disks.count}×${d.procurement.disks.model} ${d.procurement.disks.sizeGB}GB`],
    ['网卡', `${d.inventory.nics.model} fw ${d.inventory.nics.firmware}`],
  ]));
  body.appendChild(grid);

  // Template
  body.appendChild(el('div', 'section-title', '机型模板'));
  const tpl = el('div', 'mv-box');
  tpl.innerHTML = `<div class="kv"><span>阵列</span><span>${d.template.raid.level} (≥${d.template.raid.minMembers} 盘, ${d.template.raid.controllerMode})</span></div>
    <div class="kv"><span>镜像</span><span>${d.template.imageName} ${d.template.imageGB}GB</span></div>
    <div class="kv"><span>网卡固件要求</span><span>≥ ${d.template.requiresNicFirmware}</span></div>`;
  body.appendChild(tpl);

  // Audit issues
  if (d.issues && d.issues.length) {
    body.appendChild(el('div', 'section-title', '预检问题'));
    const ib = el('div', 'mv-box');
    d.issues.forEach(i => {
      const x = el('div', 'issue');
      x.appendChild(el('div', 'desc', i.detail));
      x.appendChild(el('div', 'fix', `修复方式: ${repairText(i.repair)} · 期望 ${i.want} / 实得 ${i.got}`));
      ib.appendChild(x);
    });
    body.appendChild(ib);
  }

  // Log
  body.appendChild(el('div', 'section-title', '逐步交互记录'));
  const lb = el('div', 'mv-box log');
  d.log.forEach(e => {
    const line = el('div', 'log-line');
    line.appendChild(el('div', 't', fmtDur(e.t)));
    line.appendChild(el('div', 'p', e.proto));
    line.appendChild(el('div', 's ' + (e.severity || 'info'), e.severity === 'error' ? 'ERR' : e.severity === 'warn' ? 'WRN' : ''));
    line.appendChild(el('div', 'm', e.line));
    lb.appendChild(line);
  });
  body.appendChild(lb);

  function box(title, rows) {
    const b = el('div', 'mv-box');
    b.appendChild(el('h3', '', title));
    rows.forEach(([k, v]) => {
      const kv = el('div', 'kv');
      kv.appendChild(el('span', '', k));
      kv.appendChild(el('span', '', v));
      b.appendChild(kv);
    });
    return b;
  }
}

function repairText(r) {
  return {physical:'物理处理(开箱/换件)', remote:'远程 BMC 下发', switch:'交换机/网络整改', template:'修模板'}[r] || r;
}

async function openCabinet(id) {
  const d = await getJSON(`/api/cabinet/${state.kind}/${id}`);
  openDrawer(`CAB-${String(id).padStart(2,'0')} · ${d.hosts.length} 台`);
  const body = $('#drawer-body');
  body.innerHTML = '';

  const sw = d.switch;
  const swBox = el('div', 'mv-box');
  swBox.innerHTML = `<h3>机柜交换机 ${sw.id}</h3>
    <div class="kv"><span>启动 VLAN</span><span class="${sw.bootVlanEnabled ? 'good' : 'bad'}">${sw.bootVlan} ${sw.bootVlanEnabled ? '已放行' : '未放行'}</span></div>
    <div class="kv"><span>启动会话上限</span><span>${sw.maxBootSessions}</span></div>`;
  body.appendChild(swBox);

  if (d.faults.length) {
    body.appendChild(el('div', 'section-title', '该柜故障分布'));
    const fb = el('div', 'mv-box');
    d.faults.forEach(f => {
      const kv = el('div', 'kv');
      kv.innerHTML = `<span>${f.label} <small style="color:var(--muted)">${f.category}</small></span><span class="bad">${f.count}</span>`;
      fb.appendChild(kv);
    });
    body.appendChild(fb);
  }

  body.appendChild(el('div', 'section-title', '该柜主机'));
  const tbl = el('table', 'cmp');
  tbl.innerHTML = '<thead><tr><th>主机</th><th>U位</th><th>阶段</th><th>真值</th><th>工具归因</th></tr></thead>';
  const tb = el('tbody');
  d.hosts.forEach(h => {
    const tr = el('tr', 'clickable');
    tr.innerHTML = `<td>${h.hostname}</td><td>${h.rackU}</td><td>${PHASE_LABEL[h.phase]}</td>
      <td>${h.truth ? (truthLabel(h.truth)) : '正常'}</td>
      <td>${h.fault ? (FAULT_LABEL[h.fault] || h.fault) : '—'}</td>`;
    tr.onclick = () => openMachine(h.id);
    tb.appendChild(tr);
  });
  tbl.appendChild(tb);
  body.appendChild(tbl);
}

const ROOT_LABEL = {
  'seeded.boot.order':'启动顺序:本地盘优先', 'seeded.boot.pxe.disabled':'网卡PXE禁用',
  'seeded.raid.mode':'控制器AHCI模式', 'seeded.raid.config':'残留阵列',
  'seeded.dimm.short':'内存少插', 'seeded.dimm.model':'内存型号错误',
  'seeded.disk.model':'硬盘型号错误', 'seeded.disk.short':'硬盘少一块', 'seeded.disk.bad':'硬盘SMART故障',
  'seeded.nic.firmware':'网卡固件过旧', 'seeded.nic.model':'网卡型号错误', 'seeded.mem.ecc':'内存ECC错误',
  'seeded.switch.off':'机柜交换机未放行', 'seeded.switch.cap':'交换机会话上限低',
  'seeded.image.corrupt':'镜像校验失败', 'seeded.image.slow':'镜像慢', 'seeded.install.script':'装机脚本缺陷',
};
function truthLabel(code) { return ROOT_LABEL[code] || code; }

boot();
// Deep-link: ?m=2 opens a machine drawer, ?c=3 a cabinet, after load.
setTimeout(async () => {
  const q = new URLSearchParams(location.search);
  if (q.has('m')) openMachine(Number(q.get('m')));
  if (q.has('c')) openCabinet(Number(q.get('c')));
}, 500);
