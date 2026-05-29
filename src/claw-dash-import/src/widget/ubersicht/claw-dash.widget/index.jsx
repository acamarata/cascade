// Claw Dash — Übersicht desktop widget.
// Shows an overview of the entire Claude setup: GCI stats + all projects.
// Reads from ~/.claw-dash/cache.json. Redraws every 5s.

export const refreshFrequency = 1000;

export const command = `python3 -c "
import json,os,sys
h=os.path.expanduser
cf=h('~/.claw-dash/cache.json')
pf=h('~/.claw-dash/port')
d=json.load(open(cf)) if os.path.exists(cf) else {}
d['_port']=open(pf).read().strip() if os.path.exists(pf) else '3077'
print(json.dumps(d))
" 2>/dev/null || echo '{}'`;

// ─── helpers ─────────────────────────────────────────────────────────────────
function trunc(str, max) {
  if (!str) return "";
  return str.length <= max ? str : str.slice(0, max - 1) + "…";
}

function num(v) { return (typeof v === "number" ? v : (parseInt(v, 10) || 0)); }

// ─── state ────────────────────────────────────────────────────────────────────
export const initialState = {
  top: 120, left: 40,
  dragging: false, initialized: false,
  dragStartMouseX: 0, dragStartMouseY: 0,
  dragStartTop: 0, dragStartLeft: 0,
  output: "", error: null,
  lastGoodOutput: "",
};

export const updateState = (event, prev) => {
  if (event && (
    Object.prototype.hasOwnProperty.call(event, "output") ||
    Object.prototype.hasOwnProperty.call(event, "error")
  )) {
    // Only promote to lastGoodOutput when the new output parses and has real widget data.
    var nextOutput = event.output || "";
    var nextLast = prev.lastGoodOutput;
    if (nextOutput) {
      try {
        var parsed = JSON.parse(nextOutput);
        if (parsed && parsed._widget && parsed._widget.projects && parsed._widget.projects.length > 0) {
          nextLast = nextOutput;
        }
      } catch (e) {}
    }
    return { ...prev, output: nextOutput, error: event.error, lastGoodOutput: nextLast };
  }
  if (event.type === "INIT_POS_IF_NEEDED") {
    if (prev.initialized) return prev;
    return { ...prev, top: event.top, left: event.left, initialized: true };
  }
  if (event.type === "DRAG_START") {
    return {
      ...prev, dragging: true,
      dragStartMouseX: event.mouseX, dragStartMouseY: event.mouseY,
      dragStartTop: prev.top, dragStartLeft: prev.left,
    };
  }
  if (event.type === "DRAG_MOVE" && prev.dragging) {
    const dx = event.mouseX - prev.dragStartMouseX;
    const dy = event.mouseY - prev.dragStartMouseY;
    return { ...prev, top: prev.dragStartTop + dy, left: prev.dragStartLeft + dx };
  }
  if (event.type === "DRAG_END" && prev.dragging) {
    const vw = (typeof window !== "undefined" && window.innerWidth)  || 1440;
    const vh = (typeof window !== "undefined" && window.innerHeight) || 900;
    const WIDGET_W = 345;
    const MIN_VIS  = 40;
    let x = prev.left, y = prev.top;
    if (x < -(WIDGET_W - MIN_VIS)) x = -(WIDGET_W - MIN_VIS);
    if (x > vw - MIN_VIS)          x = vw - MIN_VIS;
    if (y < 0)                     y = 0;
    if (y > vh - MIN_VIS)          y = vh - MIN_VIS;
    const cmd = `mkdir -p ~/.config/claw-dash && ` +
                `printf '{"top":%d,"left":%d}' ${y} ${x} > ` +
                `~/.config/claw-dash/widget-position.json`;
    if (typeof run === "function") { try { run(cmd); } catch (e) {} }
    return { ...prev, top: y, left: x, dragging: false };
  }
  return prev;
};

// ─── render ──────────────────────────────────────────────────────────────────
export const render = (props, dispatch) => {
  const safeProps = props || {};
  const top  = typeof safeProps.top  === "number" ? safeProps.top  : 120;
  const left = typeof safeProps.left === "number" ? safeProps.left : 40;

  // Restore saved position on first render
  if (!safeProps.initialized && dispatch) {
    try {
      run(`cat ~/.config/claw-dash/widget-position.json 2>/dev/null || echo '{}'`, function(out) {
        try {
          const pos = JSON.parse(out || "{}");
          if (typeof pos.top === "number" && typeof pos.left === "number") {
            dispatch({ type: "INIT_POS_IF_NEEDED", top: pos.top, left: pos.left });
          }
        } catch (e) {}
      });
    } catch (e) {}
  }

  const onMouseDown = function(e) {
    if (!dispatch) return;
    e.preventDefault();
    dispatch({ type: "DRAG_START", mouseX: e.clientX, mouseY: e.clientY });
    const move = function(ev) { dispatch({ type: "DRAG_MOVE", mouseX: ev.clientX, mouseY: ev.clientY }); };
    const up = function() {
      window.removeEventListener("mousemove", move);
      window.removeEventListener("mouseup", up);
      dispatch({ type: "DRAG_END" });
    };
    window.addEventListener("mousemove", move);
    window.addEventListener("mouseup", up);
  };

  if (safeProps.error) {
    return (
      <div className="shell" style={{ top: top + "px", left: left + "px" }}>
        <div className="hdr"><span className="title">claw-dash</span></div>
        <div className="err">⚠ {String(safeProps.error)}</div>
      </div>
    );
  }

  // Use current output if it has real widget data; otherwise fall back to last known good.
  var rawOutput = safeProps.output || "";
  var usingStale = false;
  var data = {}, parseErr = null;
  try {
    var parsed = rawOutput ? JSON.parse(rawOutput) : null;
    if (parsed && parsed._widget && parsed._widget.projects && parsed._widget.projects.length > 0) {
      data = parsed;
    } else {
      // Fall back to last good snapshot
      var fallback = safeProps.lastGoodOutput || "";
      if (fallback) {
        try { data = JSON.parse(fallback); usingStale = true; } catch (e) {}
      }
    }
  } catch (e) {
    parseErr = e.message;
    var fallback2 = safeProps.lastGoodOutput || "";
    if (fallback2) {
      try { data = JSON.parse(fallback2); usingStale = true; } catch (e2) {}
    }
  }

  const w      = data._widget || {};
  const port   = data._port || "3077";
  const genAt  = data.generated_at || null;
  const now    = Math.floor(Date.now() / 1000);
  const ageSec = genAt ? Math.max(0, now - genAt) : null;

  const freshnessColor =
    ageSec == null ? "#7A7F8A" :
    ageSec < 30    ? "#7A7F8A" :
    ageSec < 90    ? "#F5A623" :
                     "#E5484D";
  const freshnessLabel =
    ageSec == null ? "—"                              :
    ageSec < 5     ? "just now"                       :
    ageSec < 60    ? ageSec + "s ago"                 :
                     Math.floor(ageSec / 60) + "m ago";

  const gci      = w.gci      || {};
  const projects = w.projects || [];
  const totals   = w.totals   || {};

  const hasData = projects.length > 0 || (w.active_project);

  return (
    <div className="shell" style={{ top: top + "px", left: left + "px" }}>

      {/* ── header ── */}
      <div className="hdr" onMouseDown={onMouseDown}>
        <span className="title">Claw Dash</span>
        <span className="sub" style={{ color: freshnessColor }}>
          <span className="pulse" style={{ background: freshnessColor }}></span>
          {freshnessLabel}
        </span>
      </div>

      {parseErr && <div className="err">parse: {parseErr}</div>}

      {/* ── Global section ── */}
      {(gci.rules || gci.instructions || gci.memory || gci.references) ? (
        <div className="global-section">
          <div className="global-hdr">Global</div>
          <div className="global-cells">
            <div className="global-cell">
              <span className="global-val">{num(gci.rules)}</span>
              <span className="global-lbl">Rules</span>
            </div>
            <div className="global-cell">
              <span className="global-val">{num(gci.instructions || 0)}</span>
              <span className="global-lbl">Instructions</span>
            </div>
            <div className="global-cell">
              <span className="global-val">{num(gci.memory)}</span>
              <span className="global-lbl">Memories</span>
            </div>
            <div className="global-cell">
              <span className="global-val">{num(gci.references)}</span>
              <span className="global-lbl">References</span>
            </div>
          </div>
        </div>
      ) : null}

      {!hasData ? (
        <div className="empty">Starting up…</div>
      ) : (
        <div className="tbl-wrap">
          {/* ── column headers ── */}
          <div className="tbl-hdr">
            <span className="col-proj">PROJECT</span>
            <span className="col-n">ACT</span>
            <span className="col-n">PLN</span>
            <span className="col-n">REV</span>
            <span className="col-n">BLK</span>
            <span className="col-n">IN</span>
            <span className="col-n">IDX</span>
          </div>

          {/* ── project rows ── */}
          {projects.map(function(p, i) {
            var isActive = (p.label === w.active_project);
            return (
              <div key={i} className={"tbl-row" + (isActive ? " row-active" : "")}>
                <span className="col-proj" title={p.label}>{trunc(p.label, 11)}</span>
                <span className="col-n" style={{ color: p.active  > 0 ? "#F5A623" : "#4A4F5A" }}>{p.active  > 0 ? p.active  : "—"}</span>
                <span className="col-n" style={{ color: p.planned > 0 ? "#9CA3AF" : "#4A4F5A" }}>{p.planned > 0 ? p.planned : "—"}</span>
                <span className="col-n" style={{ color: p.review  > 0 ? "#6366F1" : "#4A4F5A" }}>{p.review  > 0 ? p.review  : "—"}</span>
                <span className="col-n" style={{ color: p.blocked > 0 ? "#E5484D" : "#4A4F5A" }}>{p.blocked > 0 ? p.blocked : "—"}</span>
                <span className="col-n" style={{ color: p.inbox   > 0 ? "#E5484D" : "#4A4F5A" }}>{p.inbox   > 0 ? p.inbox   : "—"}</span>
                <span className="col-n" style={{ color: p.ideas   > 0 ? "#7A7F8A" : "#4A4F5A" }}>{p.ideas   > 0 ? p.ideas   : "—"}</span>
              </div>
            );
          })}

          {/* ── totals row ── */}
          {projects.length > 1 ? (
            <div className="tbl-row tbl-totals">
              <span className="col-proj">TOTAL</span>
              <span className="col-n" style={{ color: totals.active  > 0 ? "#F5A623" : "#4A4F5A" }}>{totals.active  > 0 ? totals.active  : "—"}</span>
              <span className="col-n" style={{ color: totals.planned > 0 ? "#9CA3AF" : "#4A4F5A" }}>{totals.planned > 0 ? totals.planned : "—"}</span>
              <span className="col-n" style={{ color: totals.review  > 0 ? "#6366F1" : "#4A4F5A" }}>{totals.review  > 0 ? totals.review  : "—"}</span>
              <span className="col-n" style={{ color: totals.blocked > 0 ? "#E5484D" : "#4A4F5A" }}>{totals.blocked > 0 ? totals.blocked : "—"}</span>
              <span className="col-n" style={{ color: totals.inbox   > 0 ? "#E5484D" : "#4A4F5A" }}>{totals.inbox   > 0 ? totals.inbox   : "—"}</span>
              <span className="col-n" style={{ color: totals.ideas   > 0 ? "#7A7F8A" : "#4A4F5A" }}>{totals.ideas   > 0 ? totals.ideas   : "—"}</span>
            </div>
          ) : null}

        </div>
      )}

      {/* ── footer ── */}
      <div className="footer-row">
        <span className="footer-left">
          {w.active_project ? (
            <span className="active-proj-label">{trunc(w.active_project, 14)}</span>
          ) : null}
          {w.active_phase ? (
            <span className="active-phase-label">{trunc(w.active_phase, 22)}</span>
          ) : null}
        </span>
        <span
          className="open-link"
          onClick={function() {
            if (typeof run === "function") {
              try { run("open http://localhost:" + port); } catch (e) {}
            }
          }}
        >
          Open →
        </span>
      </div>

    </div>
  );
};

export const className = `
  top: 0; left: 0; width: 0; height: 0; z-index: 1;
  user-select: none;
  font-family: -apple-system, "SF Pro Text", "Helvetica Neue", system-ui;
  -webkit-font-smoothing: antialiased;

  .shell {
    position: absolute;
    width: 345px;
    color: #E6E6E6;
    background: rgba(14, 16, 24, 0.76);
    backdrop-filter: blur(20px) saturate(1.6);
    -webkit-backdrop-filter: blur(20px) saturate(1.6);
    border: 1px solid rgba(255,255,255,0.08);
    border-radius: 18px;
    padding: 12px 14px 10px;
    box-shadow: 0 10px 40px rgba(0,0,0,0.45), inset 0 1px 0 rgba(255,255,255,0.05);
  }

  .hdr {
    display: flex; justify-content: space-between; align-items: baseline;
    margin-bottom: 8px; cursor: grab;
  }
  .hdr:active { cursor: grabbing; }
  .title { font-weight: 600; font-size: 12px; letter-spacing: 0.02em; }
  .sub   { color: #7A7F8A; font-size: 10px; font-weight: 400;
           display: inline-flex; align-items: center; gap: 5px; }
  .pulse {
    display: inline-block; width: 6px; height: 6px; border-radius: 50%;
    animation: cd-pulse 1.6s ease-in-out infinite;
  }
  @keyframes cd-pulse {
    0%, 100% { opacity: 0.35; transform: scale(0.85); }
    50%       { opacity: 1.0;  transform: scale(1.0);  }
  }

  /* Global section */
  .global-section {
    padding: 6px 0;
    border-top: 1px solid rgba(255,255,255,0.08);
    border-bottom: 1px solid rgba(255,255,255,0.08);
    margin-bottom: 4px;
  }
  .global-hdr {
    font-size: 9px; font-weight: 700; letter-spacing: 0.08em;
    color: #4A4F5A; text-transform: uppercase; margin-bottom: 5px;
  }
  .global-cells {
    display: flex;
  }
  .global-cell {
    flex: 1;
    display: flex; flex-direction: column; align-items: center; gap: 2px;
    border-right: 1px solid rgba(255,255,255,0.06);
    padding: 2px 0;
  }
  .global-cell:last-child { border-right: none; }
  .global-val {
    font-size: 14px; font-weight: 700; color: #C4C4C4;
    font-variant-numeric: tabular-nums; line-height: 1;
  }
  .global-lbl {
    font-size: 9px; color: #4A4F5A; letter-spacing: 0.04em;
  }

  /* Table */
  .tbl-wrap {
    display: flex; flex-direction: column;
  }

  .tbl-hdr, .tbl-row {
    display: flex; align-items: center;
    padding: 2px 0;
  }
  .tbl-hdr {
    border-bottom: 1px solid rgba(255,255,255,0.08);
    padding-bottom: 4px; margin-bottom: 1px;
    font-size: 9px; font-weight: 700; letter-spacing: 0.06em;
    color: #4A4F5A; text-transform: uppercase;
  }
  .tbl-row { font-size: 11px; }
  .tbl-row.row-active .col-proj { color: #E6E6E6; font-weight: 600; }
  .tbl-totals {
    border-top: 1px solid rgba(255,255,255,0.08);
    margin-top: 2px; padding-top: 4px;
    font-size: 10px;
  }
  .tbl-totals .col-proj { color: #4A4F5A; font-size: 9px; font-weight: 700;
                           letter-spacing: 0.06em; text-transform: uppercase; }

  .col-proj {
    flex: 1; color: #9CA3AF;
    white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
    padding-right: 6px;
  }
  .col-n {
    width: 28px; text-align: right; flex-shrink: 0;
    font-variant-numeric: tabular-nums;
  }

  /* Footer */
  .footer-row {
    display: flex; align-items: center; justify-content: space-between;
    padding-top: 7px; margin-top: 3px;
    border-top: 1px solid rgba(255,255,255,0.08);
  }
  .footer-left { display: flex; flex-direction: column; gap: 1px; flex: 1; min-width: 0; overflow: hidden; }
  .active-proj-label {
    font-size: 10px; color: #C4C4C4; font-weight: 600;
    white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  }
  .active-phase-label {
    font-size: 9px; color: #5A5F6A;
    white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  }
  .open-link {
    font-size: 10px; color: #7A7F8A; cursor: pointer;
    transition: color 120ms ease; flex-shrink: 0; padding-left: 8px;
  }
  .open-link:hover { color: #E6E6E6; }

  .err   { color: #FB7185; padding: 6px 0; font-size: 10px; }
  .empty { color: #9CA3AF; padding: 8px 0; font-size: 11px; font-style: italic; }
`;
