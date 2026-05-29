// cascade-dash — full dashboard application
// Vanilla JS, no framework, no external dependencies

'use strict';

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

let cache = null;
let selectedProject = 0;
let selectedThread = null;
let activeView = 'kanban';
let expandedMemoryFile = null;
let expandedInboxMsg = null;
let lastUpdated = null;
let lastGeneratedAt = null;
let tickInterval = null;

// GCI tab state
let gciSubView = 'map';    // 'map' | 'list'
let gciCategory = null;    // currently selected category key
let gciFile = null;        // currently selected file name
let gciEditing = false;    // is the file editor open?
let gciData = null;        // cached /api/gci index
let gciFileContent = null; // { cat, name, raw, mtime, cites }

// ---------------------------------------------------------------------------
// Utilities
// ---------------------------------------------------------------------------

function esc(s) {
  return String(s)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

function ago(ts) {
  if (!ts) return '';
  const diff = Math.floor((Date.now() - new Date(ts).getTime()) / 1000);
  if (diff < 5) return 'just now';
  if (diff < 60) return `${diff}s ago`;
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
  return `${Math.floor(diff / 86400)}d ago`;
}

function fmtDate(ts) {
  if (!ts) return '';
  const d = new Date(ts);
  const months = ['Jan','Feb','Mar','Apr','May','Jun','Jul','Aug','Sep','Oct','Nov','Dec'];
  const h = d.getHours();
  const m = String(d.getMinutes()).padStart(2, '0');
  const ampm = h >= 12 ? 'p' : 'a';
  const h12 = h % 12 || 12;
  return `${months[d.getMonth()]} ${d.getDate()} ${h12}:${m}${ampm}`;
}

function taskStateClass(line) {
  if (/^🚧/.test(line)) return 'state-active';
  if (/^🔲/.test(line)) return 'state-planned';
  if (/^⏳/.test(line)) return 'state-ready';
  if (/^👀/.test(line)) return 'state-review';
  if (/^✅/.test(line)) return 'state-done';
  if (/^🔒/.test(line)) return 'state-blocked';
  if (/^❌/.test(line)) return 'state-failed';
  if (/^⏸️/.test(line)) return 'state-paused';
  if (/^🚫/.test(line)) return 'state-canceled';
  return '';
}

function taskStateFromLine(line) {
  if (/^🚧/.test(line)) return 'active';
  if (/^🔲/.test(line)) return 'planned';
  if (/^⏳/.test(line)) return 'ready';
  if (/^👀/.test(line)) return 'review';
  if (/^✅/.test(line)) return 'done';
  if (/^🔒/.test(line)) return 'blocked';
  if (/^❌/.test(line)) return 'failed';
  if (/^⏸️/.test(line)) return 'paused';
  if (/^🚫/.test(line)) return 'canceled';
  return '';
}

// ---------------------------------------------------------------------------
// Markdown renderer (inline, no deps)
// ---------------------------------------------------------------------------

let _copyIdCounter = 0;

function renderMarkdown(text) {
  if (!text) return '';

  // Collect code blocks first (replace with placeholders)
  const codeBlocks = [];
  let html = text.replace(/```([^\n]*)\n([\s\S]*?)```/g, (_, lang, code) => {
    const id = 'cb' + (_copyIdCounter++);
    codeBlocks.push({ id, lang: lang.trim(), code });
    return `\x00CODE_BLOCK_${codeBlocks.length - 1}\x00`;
  });

  // Inline code (before other inline transforms)
  html = html.replace(/`([^`]+)`/g, (_, c) => `<code class="md-inline-code">${esc(c)}</code>`);

  // Escape HTML in non-code content (we already used esc inside inline code)
  // We need to escape the non-placeholder, non-inline-code parts.
  // Since inline code is already escaped and wrapped in tags, we just need
  // to escape remaining raw text. We'll do a targeted pass.
  // Split on our inline code and code block placeholders to escape only prose.
  html = html.replace(/(?<!<code[^>]*>)(?<!<\/code>)([^<\x00]+)/g, (match) => {
    // Only escape text nodes (no tags, no placeholders)
    if (/\x00/.test(match)) return match;
    return match
      .replace(/&(?!amp;|lt;|gt;|quot;)/g, '&amp;')
      .replace(/<(?!code|\/code)/g, '&lt;');
  });

  // Headers
  html = html.replace(/^#{4}\s+(.+)$/gm, '<h4>$1</h4>');
  html = html.replace(/^#{3}\s+(.+)$/gm, '<h3>$1</h3>');
  html = html.replace(/^#{2}\s+(.+)$/gm, '<h2>$1</h2>');
  html = html.replace(/^#{1}\s+(.+)$/gm, '<h1>$1</h1>');

  // Bold and italic
  html = html.replace(/\*\*\*(.+?)\*\*\*/g, '<strong><em>$1</em></strong>');
  html = html.replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>');
  html = html.replace(/\*(.+?)\*/g, '<em>$1</em>');
  html = html.replace(/___(.+?)___/g, '<strong><em>$1</em></strong>');
  html = html.replace(/__(.+?)__/g, '<strong>$1</strong>');
  html = html.replace(/_([^_]+)_/g, '<em>$1</em>');

  // Horizontal rules
  html = html.replace(/^---+$/gm, '<hr>');
  html = html.replace(/^\*\*\*+$/gm, '<hr>');

  // Links
  html = html.replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2" target="_blank" rel="noopener noreferrer">$1</a>');

  // Lists — process block by block
  html = html.replace(/((?:^[ \t]*[-*]\s.+\n?)+)/gm, (block) => {
    const items = block.trim().split('\n').map(line =>
      `<li>${line.replace(/^[ \t]*[-*]\s/, '').trim()}</li>`
    ).join('');
    return `<ul>${items}</ul>`;
  });

  html = html.replace(/((?:^\d+\.\s.+\n?)+)/gm, (block) => {
    const items = block.trim().split('\n').map(line =>
      `<li>${line.replace(/^\d+\.\s/, '').trim()}</li>`
    ).join('');
    return `<ol>${items}</ol>`;
  });

  // Paragraphs (blank-line-separated prose that isn't already a block element)
  const blockTags = /^<(h[1-4]|ul|ol|li|hr|pre|blockquote)/;
  const lines = html.split('\n');
  const out = [];
  let para = [];

  const flushPara = () => {
    if (para.length) {
      const joined = para.join(' ').trim();
      if (joined && !blockTags.test(joined)) {
        out.push(`<p>${joined}</p>`);
      } else if (joined) {
        out.push(joined);
      }
      para = [];
    }
  };

  for (const line of lines) {
    if (/^\x00CODE_BLOCK_/.test(line)) {
      flushPara();
      out.push(line);
    } else if (line.trim() === '') {
      flushPara();
    } else if (blockTags.test(line.trim())) {
      flushPara();
      out.push(line);
    } else {
      para.push(line);
    }
  }
  flushPara();
  html = out.join('\n');

  // Restore code blocks
  codeBlocks.forEach((cb, i) => {
    const copyId = `code-copy-${cb.id}`;
    const block =
      `<div class="md-pre-wrap">` +
      `<button class="copy-btn code-copy" data-copy-code="${esc(cb.code)}" title="Copy code">copy</button>` +
      `<pre class="md-pre"><code class="language-${esc(cb.lang)}">${esc(cb.code)}</code></pre>` +
      `</div>`;
    html = html.replace(`\x00CODE_BLOCK_${i}\x00`, block);
  });

  return html;
}

// ---------------------------------------------------------------------------
// Copy helper
// ---------------------------------------------------------------------------

function handleCopy(text, btn) {
  navigator.clipboard.writeText(text).then(() => {
    const orig = btn.textContent;
    btn.textContent = '✓';
    btn.classList.add('copied');
    setTimeout(() => {
      btn.textContent = orig;
      btn.classList.remove('copied');
    }, 1500);
  }).catch(() => {
    // Fallback: select text in a temp textarea
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.cssText = 'position:fixed;top:-9999px;left:-9999px;opacity:0';
    document.body.appendChild(ta);
    ta.select();
    try { document.execCommand('copy'); } catch (_) {}
    document.body.removeChild(ta);
    btn.textContent = '✓';
    btn.classList.add('copied');
    setTimeout(() => {
      btn.textContent = 'copy';
      btn.classList.remove('copied');
    }, 1500);
  });
}

// ---------------------------------------------------------------------------
// Data fetching
// ---------------------------------------------------------------------------

async function fetchCache() {
  try {
    const res = await fetch('/api/cache');
    if (!res.ok) return;
    const data = await res.json();
    if (data.generated_at !== lastGeneratedAt) {
      lastGeneratedAt = data.generated_at;
      cache = data;
      lastUpdated = new Date();
      render();
    }
  } catch (_) {
    // Server not ready yet — silent
  }
}

// ---------------------------------------------------------------------------
// Project helpers
// ---------------------------------------------------------------------------

function currentProject() {
  if (!cache || !cache.projects || !cache.projects.length) return null;
  return cache.projects[Math.min(selectedProject, cache.projects.length - 1)];
}

function inboxCount(proj) {
  if (!proj || !proj.inbox) return 0;
  return proj.inbox.length;
}

function ideasCount(proj) {
  if (!proj || !proj.ideas) return 0;
  return proj.ideas.length;
}

// ---------------------------------------------------------------------------
// Rendering — Top Bar
// ---------------------------------------------------------------------------

function renderTopbar() {
  const proj = currentProject();
  const projects = (cache && cache.projects) ? cache.projects : [];

  const projectOptions = projects.map((p, i) =>
    `<option value="${i}" ${i === selectedProject ? 'selected' : ''}>${esc(p.name || p.path || `Project ${i}`)}</option>`
  ).join('');

  const unread = proj ? inboxCount(proj) : 0;
  const inboxBadge = unread ? `<span class="inbox-badge">${unread}</span>` : '';
  const ideasBadge = proj ? ideasCount(proj) : 0;

  const tabs = [
    { id: 'kanban',  label: 'Kanban' },
    { id: 'memory',  label: 'Memory' },
    { id: 'inbox',   label: `Inbox${inboxBadge}` },
    { id: 'ideas',   label: `Ideas${ideasBadge ? `<span class="inbox-badge ideas-badge">${ideasBadge}</span>` : ''}` },
    { id: 'active',  label: 'active.md' },
    { id: 'gci',     label: 'GCI' },
  ].map(t =>
    `<button class="tab-btn ${activeView === t.id ? 'active' : ''}" data-view="${t.id}">${t.label}</button>`
  ).join('');

  const updatedText = lastUpdated ? ago(lastUpdated.toISOString()) : '—';

  document.getElementById('topbar').innerHTML =
    `<div class="topbar-left">
       <span class="brand-icon">⚡</span>
       <span class="brand-name">cascade-dash</span>
       ${projects.length > 1
         ? `<select class="project-picker" data-action="pick-project">${projectOptions}</select>`
         : proj ? `<span class="project-name-static">${esc(proj.name || proj.path || '')}</span>` : ''
       }
     </div>
     <div class="topbar-tabs">${tabs}</div>
     <div class="topbar-right">
       <span class="pulse-dot" title="Live"></span>
       <span class="updated-text" id="updated-text">Updated ${esc(updatedText)}</span>
       <button class="refresh-btn" data-action="refresh" title="Refresh now">↻</button>
     </div>`;
}

// ---------------------------------------------------------------------------
// Rendering — Sidebar
// ---------------------------------------------------------------------------

function renderSidebar() {
  const proj = currentProject();
  const sidebar = document.getElementById('sidebar');

  // GCI tab has its own full-width layout — no sidebar needed
  if (activeView === 'gci') {
    sidebar.innerHTML = '';
    sidebar.style.display = 'none';
    return;
  }

  if (!proj || !proj.threads || !Object.keys(proj.threads).length) {
    sidebar.innerHTML = '';
    sidebar.style.display = 'none';
    return;
  }

  sidebar.style.display = '';

  const threadNames = Object.keys(proj.threads);
  const allItem =
    `<div class="thread-item ${selectedThread === null ? 'active' : ''}" data-thread="">
       <span class="thread-name">All tasks</span>
     </div>`;

  const items = threadNames.map(name => {
    const t = proj.threads[name];
    const total =
      (t.planned ? t.planned.length : 0) +
      (t.open ? t.open.length : 0) +
      (t.review ? t.review.length : 0) +
      (t.done ? t.done.length : 0);
    const open = (t.open ? t.open.length : 0) + (t.review ? t.review.length : 0);
    return `<div class="thread-item ${selectedThread === name ? 'active' : ''}" data-thread="${esc(name)}">
      <span class="thread-name">${esc(name)}</span>
      <span class="thread-pill">${open}/${total}</span>
    </div>`;
  }).join('');

  sidebar.innerHTML =
    `<div class="sidebar-header">Threads</div>
     ${allItem}
     ${items}`;
}

// ---------------------------------------------------------------------------
// Rendering — Kanban
// ---------------------------------------------------------------------------

function buildTaskCard(task, idx) {
  const line = typeof task === 'string' ? task : (task.line || task.title || '');
  const display = line.length > 80 ? line.slice(0, 80) + '…' : line;
  const stateClass = taskStateClass(line);
  return `<div class="task-card ${stateClass}">
    <span class="task-line">${esc(display)}</span>
    <button class="copy-btn task-copy" data-copy-text="${esc(line)}" title="Copy task">copy</button>
  </div>`;
}

function buildColumn(title, tasks, colId) {
  const cards = (tasks || []).map((t, i) => buildTaskCard(t, i)).join('');
  const allText = (tasks || []).map(t => {
    const line = typeof t === 'string' ? t : (t.line || t.title || '');
    return `- ${line}`;
  }).join('\n');

  return `<div class="kanban-col col-${colId}">
    <div class="kanban-col-header">
      <span class="col-title">${title}</span>
      <span class="col-count">${(tasks || []).length}</span>
      ${allText ? `<button class="copy-btn col-copy" data-copy-text="${esc(allText)}" title="Copy all">copy all</button>` : ''}
    </div>
    <div class="kanban-col-body">${cards || '<div class="empty-col">—</div>'}</div>
  </div>`;
}

function renderKanban() {
  const proj = currentProject();
  const main = document.getElementById('main');

  if (!proj) {
    main.innerHTML = '<div class="empty-state">No project data. Is the poller running?</div>';
    return;
  }

  let planned = [], active = [], review = [], done = [];

  if (selectedThread && proj.threads && proj.threads[selectedThread]) {
    const t = proj.threads[selectedThread];
    planned = t.planned || [];
    active  = t.open    || [];
    review  = t.review  || [];
    done    = t.done    || [];
  } else {
    // Use active.tasks filtered by state
    const tasks = (proj.active && proj.active.tasks) ? proj.active.tasks : [];
    tasks.forEach(task => {
      const line = typeof task === 'string' ? task : (task.line || '');
      const state = taskStateFromLine(line);
      if (state === 'planned' || state === 'ready') planned.push(task);
      else if (state === 'active') active.push(task);
      else if (state === 'review') review.push(task);
      else if (state === 'done') done.push(task);
    });
  }

  main.innerHTML =
    `<div class="kanban">
      ${buildColumn('PLANNED', planned, 'planned')}
      ${buildColumn('ACTIVE', active, 'active')}
      ${buildColumn('IN REVIEW', review, 'review')}
      ${buildColumn('DONE', done, 'done')}
    </div>`;
}

// ---------------------------------------------------------------------------
// Rendering — Memory
// ---------------------------------------------------------------------------

function renderMemory() {
  const proj = currentProject();
  const main = document.getElementById('main');

  if (!proj || !proj.memory) {
    main.innerHTML = '<div class="empty-state">No memory data.</div>';
    return;
  }

  const memory = proj.memory; // { index: [{filename, summary}], files: {filename: content} }
  const entries = (memory.index || []);

  const entryList = entries.map(e =>
    `<div class="memory-entry ${expandedMemoryFile === e.filename ? 'active' : ''}" data-memory-file="${esc(e.filename)}">
       <span class="memory-entry-name">${esc(e.filename)}</span>
       <span class="memory-entry-summary">${esc(e.summary || '')}</span>
       <button class="copy-btn memory-copy" data-copy-text="${esc(e.summary || '')}" title="Copy summary">copy</button>
     </div>`
  ).join('');

  let viewer = '';
  if (expandedMemoryFile && memory.files && memory.files[expandedMemoryFile]) {
    const content = memory.files[expandedMemoryFile];
    viewer =
      `<div class="memory-viewer">
         <div class="memory-viewer-header">
           <span class="memory-viewer-name">${esc(expandedMemoryFile)}</span>
           <button class="copy-btn viewer-copy" data-copy-text="${esc(content)}">copy all</button>
         </div>
         <div class="memory-viewer-body">${renderMarkdown(content)}</div>
       </div>`;
  } else {
    viewer = '<div class="memory-viewer empty-viewer"><span class="empty-state">Select a file to view.</span></div>';
  }

  main.innerHTML =
    `<div class="memory-layout">
       <div class="memory-list">
         <div class="memory-list-header">Memory Files</div>
         ${entryList || '<div class="empty-state">No memory files.</div>'}
       </div>
       ${viewer}
     </div>`;
}

// ---------------------------------------------------------------------------
// Rendering — Inbox
// ---------------------------------------------------------------------------

function renderInbox() {
  const proj = currentProject();
  const main = document.getElementById('main');

  if (!proj || !proj.inbox || !proj.inbox.length) {
    main.innerHTML = '<div class="empty-state">Inbox is empty.</div>';
    return;
  }

  const msgs = proj.inbox.slice().sort((a, b) => {
    return (b.mtime || 0) - (a.mtime || 0);
  });

  const items = msgs.map(msg => {
    const isOpen = expandedInboxMsg === msg.filename;
    const preview = (msg.content || '').split('\n')[0];
    const body = isOpen
      ? `<div class="inbox-body">${renderMarkdown(msg.content || '')}</div>`
      : '';
    return `<div class="inbox-item ${isOpen ? 'open' : ''}" data-inbox-file="${esc(msg.filename)}">
      <div class="inbox-item-header">
        <span class="inbox-filename">${esc(msg.filename)}</span>
        <span class="inbox-age">${ago(msg.mtime ? new Date(msg.mtime * 1000).toISOString() : null)}</span>
        <button class="copy-btn inbox-copy" data-copy-text="${esc(msg.content || '')}" title="Copy message">copy</button>
      </div>
      <div class="inbox-preview">${esc(preview)}</div>
      ${body}
    </div>`;
  }).join('');

  main.innerHTML = `<div class="inbox-list">${items}</div>`;
}

// ---------------------------------------------------------------------------
// Rendering — Ideas
// ---------------------------------------------------------------------------

function renderIdeas() {
  const proj = currentProject();
  const main = document.getElementById('main');

  if (!proj || !proj.ideas || !proj.ideas.length) {
    main.innerHTML = '<div class="empty-state">No ideas captured yet.</div>';
    return;
  }

  const items = proj.ideas.map(idea => {
    const isOpen = expandedInboxMsg === ('idea:' + idea.filename);
    const preview = (idea.content || '').split('\n')[0];
    const body = isOpen
      ? `<div class="inbox-body">${renderMarkdown(idea.content || '')}</div>`
      : '';
    return `<div class="inbox-item ${isOpen ? 'open' : ''}" data-idea-file="${esc(idea.filename)}">
      <div class="inbox-item-header">
        <span class="inbox-filename">${esc(idea.filename)}</span>
        <button class="copy-btn inbox-copy" data-copy-text="${esc(idea.content || '')}" title="Copy idea">copy</button>
      </div>
      <div class="inbox-preview">${esc(preview)}</div>
      ${body}
    </div>`;
  }).join('');

  main.innerHTML = `<div class="inbox-list">${items}</div>`;
}

// ---------------------------------------------------------------------------
// Rendering — active.md
// ---------------------------------------------------------------------------

function renderActive() {
  const proj = currentProject();
  const main = document.getElementById('main');

  if (!proj || !proj.active || !proj.active.raw) {
    main.innerHTML = '<div class="empty-state">No active.md content.</div>';
    return;
  }

  const raw = proj.active.raw;
  main.innerHTML =
    `<div class="active-view">
       <div class="active-header">
         <span class="active-title">active.md</span>
         <button class="copy-btn active-copy" data-copy-text="${esc(raw)}">copy all</button>
       </div>
       <div class="active-body">${renderMarkdown(raw)}</div>
     </div>`;
}

// ---------------------------------------------------------------------------
// Rendering — GCI
// ---------------------------------------------------------------------------

const GCI_CAT_META = {
  rules:      { label: 'Rules',      icon: '⚖', desc: 'Hard behavioral mandates' },
  references: { label: 'References', icon: '📚', desc: 'On-demand protocol docs' },
  memory:     { label: 'Memory',     icon: '🧠', desc: 'Persistent recall entries' },
  skills:     { label: 'Skills',     icon: '🛠', desc: 'Slash command definitions' },
  agents:     { label: 'Agents',     icon: '🤖', desc: 'Specialist agent personas' },
  spine:      { label: 'Spine',      icon: '📜', desc: 'CLAUDE.md + RTK.md root files' },
};

function renderGci() {
  const main = document.getElementById('main');

  if (gciSubView === 'map' || !gciCategory) {
    // MAP VIEW — category cards
    // Use cached gciData if available for counts, otherwise show 0 until loaded
    const counts = gciData || {};
    const totalFiles = Object.values(counts).reduce((s, arr) => s + (Array.isArray(arr) ? arr.length : 0), 0);

    const cards = Object.entries(GCI_CAT_META).map(([key, meta]) => {
      const arr = counts[key] || [];
      const count = arr.length;
      const mtime = arr.length ? Math.max(...arr.map(f => f.mtime || 0)) : 0;
      const age = mtime ? Math.floor((Date.now() / 1000 - mtime) / 86400) : null;
      const ageStr = age === null ? '' : age === 0 ? 'today' : age + 'd ago';
      return `<div class="gci-card" onclick="gciDrillCat('${key}')">
        <div class="gci-card-icon">${meta.icon}</div>
        <div class="gci-card-count">${count}</div>
        <div class="gci-card-label">${meta.label}</div>
        <div class="gci-card-desc">${meta.desc}</div>
        ${ageStr ? `<div class="gci-card-age">${ageStr}</div>` : ''}
      </div>`;
    }).join('');

    main.innerHTML = `<div class="gci-wrap">
      <div class="gci-map-header">
        <span class="gci-title">Global Claude Instructions</span>
        <span class="gci-subtitle">~/.claude/ — ${totalFiles} files</span>
      </div>
      <div class="gci-cards">${cards}</div>
    </div>`;

    // If we don't have gciData yet, fetch it so cards update with real counts
    if (!gciData) fetchGciIndex();
    return;
  }

  // LIST VIEW — files in selected category
  const catMeta = GCI_CAT_META[gciCategory] || { label: gciCategory, icon: '📄' };

  // File list panel
  let fileListHtml = '';
  if (!gciData) {
    fetchGciIndex();
    fileListHtml = '<div class="gci-loading">Loading…</div>';
  } else {
    const files = gciData[gciCategory] || [];
    fileListHtml = files.map(f => {
      const sel = gciFile === f.name ? ' gci-file-sel' : '';
      const kb = (f.size / 1024).toFixed(1);
      return `<div class="gci-file-row${sel}" onclick="gciOpenFile('${esc(gciCategory)}','${esc(f.name)}')">
        <span class="gci-file-name">${esc(f.name)}</span>
        <span class="gci-file-meta">${f.lines}L · ${kb}K</span>
      </div>`;
    }).join('') || '<div class="gci-empty">No files</div>';
  }

  // File viewer / editor panel
  let viewerHtml = '';
  if (!gciFile) {
    viewerHtml = '<div class="gci-viewer-placeholder">← Select a file</div>';
  } else if (!gciFileContent || gciFileContent.name !== gciFile) {
    fetchGciFile(gciCategory, gciFile);
    viewerHtml = '<div class="gci-loading">Loading…</div>';
  } else {
    const fc = gciFileContent;
    const canDelete = gciCategory !== 'spine';
    if (gciEditing) {
      viewerHtml = `<div class="gci-viewer-hdr">
          <span class="gci-viewer-title">${esc(fc.name)}</span>
          <div class="gci-viewer-actions">
            <button class="gci-btn gci-btn-save" onclick="gciSaveFile()">Save</button>
            <button class="gci-btn" onclick="gciCancelEdit()">Cancel</button>
          </div>
        </div>
        <textarea id="gci-editor" class="gci-editor">${esc(fc.raw)}</textarea>`;
    } else {
      const citesHtml = fc.cites && fc.cites.length
        ? `<div class="gci-cites">Cites: ${fc.cites.map(c => `<span class="gci-cite-chip">${esc(c)}</span>`).join('')}</div>`
        : '';
      viewerHtml = `<div class="gci-viewer-hdr">
          <span class="gci-viewer-title">${esc(fc.name)}</span>
          <div class="gci-viewer-actions">
            <button class="gci-btn" onclick="gciStartEdit()">Edit</button>
            <button class="copy-btn gci-btn" data-copy-text="${esc(fc.raw)}" title="Copy raw">Copy</button>
            ${canDelete ? `<button class="gci-btn gci-btn-del" onclick="gciDeleteFile()">Delete</button>` : ''}
          </div>
        </div>
        ${citesHtml}
        <div class="gci-viewer-body">${renderMarkdown(fc.raw)}</div>`;
    }
  }

  main.innerHTML = `<div class="gci-wrap">
    <div class="gci-list-header">
      <button class="gci-back" onclick="gciGoMap()">← GCI</button>
      <span class="gci-list-title">${catMeta.icon} ${catMeta.label}</span>
      <button class="gci-btn gci-btn-new" onclick="gciNewFile()">+ New</button>
    </div>
    <div class="gci-list-body">
      <div class="gci-file-list">${fileListHtml}</div>
      <div class="gci-file-viewer">${viewerHtml}</div>
    </div>
  </div>`;
}

// ---------------------------------------------------------------------------
// GCI action functions
// ---------------------------------------------------------------------------

function gciDrillCat(cat) {
  gciSubView = 'list';
  gciCategory = cat;
  gciFile = null;
  gciFileContent = null;
  gciEditing = false;
  if (!gciData) fetchGciIndex();
  render();
}

function gciGoMap() {
  gciSubView = 'map';
  gciCategory = null;
  gciFile = null;
  gciFileContent = null;
  gciEditing = false;
  render();
}

function gciOpenFile(cat, name) {
  gciFile = name;
  gciFileContent = null;
  gciEditing = false;
  fetchGciFile(cat, name);
}

function fetchGciIndex() {
  fetch('/api/gci')
    .then(r => r.json())
    .then(d => { gciData = d; render(); })
    .catch(() => {});
}

function fetchGciFile(cat, name) {
  fetch('/api/gci/file?cat=' + encodeURIComponent(cat) + '&name=' + encodeURIComponent(name))
    .then(r => r.json())
    .then(d => { gciFileContent = d; render(); })
    .catch(() => {});
}

function gciStartEdit() { gciEditing = true; render(); }
function gciCancelEdit() { gciEditing = false; render(); }

function gciSaveFile() {
  const el = document.getElementById('gci-editor');
  if (!el || !gciFileContent) return;
  const body = el.value;
  fetch('/api/gci/file?cat=' + encodeURIComponent(gciCategory) + '&name=' + encodeURIComponent(gciFile), {
    method: 'PUT',
    headers: { 'Content-Type': 'text/plain' },
    body: body
  }).then(r => r.json()).then(d => {
    gciEditing = false;
    gciFileContent = { ...gciFileContent, raw: body, mtime: d.mtime };
    render();
  }).catch(() => alert('Save failed'));
}

function gciDeleteFile() {
  if (!confirm('Delete ' + gciFile + '?')) return;
  fetch('/api/gci/file?cat=' + encodeURIComponent(gciCategory) + '&name=' + encodeURIComponent(gciFile), {
    method: 'DELETE'
  }).then(r => r.json()).then(() => {
    gciFile = null;
    gciFileContent = null;
    gciData = null; // force refresh
    fetchGciIndex();
  }).catch(() => alert('Delete failed'));
}

function gciNewFile() {
  const name = prompt('File name (e.g. my-rule.md):');
  if (!name || !name.endsWith('.md')) return;
  fetch('/api/gci/file?cat=' + encodeURIComponent(gciCategory) + '&name=' + encodeURIComponent(name), {
    method: 'POST',
    headers: { 'Content-Type': 'text/plain' },
    body: '# ' + name.replace('.md', '') + '\n\n'
  }).then(r => {
    if (r.status === 409) { alert('File already exists'); return null; }
    return r.json();
  }).then(d => {
    if (!d) return;
    gciData = null;
    gciFile = name;
    gciFileContent = null;
    gciEditing = true;
    fetchGciIndex();
    fetchGciFile(gciCategory, name);
  }).catch(() => alert('Create failed'));
}

// ---------------------------------------------------------------------------
// Master render
// ---------------------------------------------------------------------------

function render() {
  if (!cache) {
    document.getElementById('topbar').innerHTML =
      `<div class="topbar-left"><span class="brand-icon">⚡</span><span class="brand-name">cascade-dash</span></div>
       <div class="topbar-right"><span class="updated-text">Waiting for data…</span></div>`;
    document.getElementById('sidebar').innerHTML = '';
    document.getElementById('main').innerHTML =
      `<div class="empty-state loading">Loading cache…<br><small>Make sure the cascade-dash poller is running.</small></div>`;
    return;
  }

  renderTopbar();
  renderSidebar();

  switch (activeView) {
    case 'kanban': renderKanban(); break;
    case 'memory': renderMemory(); break;
    case 'inbox':  renderInbox();  break;
    case 'ideas':  renderIdeas();  break;
    case 'active': renderActive(); break;
    case 'gci':    renderGci();    break;
    default:       renderKanban();
  }
}

// ---------------------------------------------------------------------------
// "Updated X ago" ticker
// ---------------------------------------------------------------------------

function startTicker() {
  if (tickInterval) return;
  tickInterval = setInterval(() => {
    const el = document.getElementById('updated-text');
    if (el && lastUpdated) {
      el.textContent = `Updated ${ago(lastUpdated.toISOString())}`;
    }
  }, 1000);
}

// ---------------------------------------------------------------------------
// Event delegation
// ---------------------------------------------------------------------------

document.addEventListener('click', (e) => {
  const t = e.target;

  // Copy buttons — check data-copy-text and data-copy-code
  if (t.classList.contains('copy-btn') || t.closest('.copy-btn')) {
    const btn = t.classList.contains('copy-btn') ? t : t.closest('.copy-btn');
    const text = btn.dataset.copyText || btn.dataset.copyCode || '';
    if (text) {
      e.stopPropagation();
      handleCopy(text, btn);
      return;
    }
  }

  // View tabs
  if (t.dataset.view) {
    activeView = t.dataset.view;
    expandedMemoryFile = null;
    expandedInboxMsg = null;
    render();
    return;
  }

  // Project picker (select change is below; this handles potential button-style picker)
  if (t.dataset.action === 'refresh') {
    fetchCache();
    return;
  }

  // Thread selection
  if (t.closest('[data-thread]') && !t.closest('.copy-btn')) {
    const item = t.closest('[data-thread]');
    const name = item.dataset.thread;
    selectedThread = name === '' ? null : name;
    render();
    return;
  }

  // Memory file selection
  if (t.closest('[data-memory-file]') && !t.closest('.copy-btn')) {
    const item = t.closest('[data-memory-file]');
    const fname = item.dataset.memoryFile;
    expandedMemoryFile = expandedMemoryFile === fname ? null : fname;
    render();
    return;
  }

  // Inbox message expand/collapse
  if (t.closest('[data-inbox-file]') && !t.closest('.copy-btn')) {
    const item = t.closest('[data-inbox-file]');
    const fname = item.dataset.inboxFile;
    expandedInboxMsg = expandedInboxMsg === fname ? null : fname;
    render();
    return;
  }

  // Idea expand/collapse
  if (t.closest('[data-idea-file]') && !t.closest('.copy-btn')) {
    const item = t.closest('[data-idea-file]');
    const fname = 'idea:' + item.dataset.ideaFile;
    expandedInboxMsg = expandedInboxMsg === fname ? null : fname;
    render();
    return;
  }
});

// Project picker change (it's a <select>)
document.addEventListener('change', (e) => {
  if (e.target.dataset.action === 'pick-project') {
    selectedProject = parseInt(e.target.value, 10) || 0;
    selectedThread = null;
    expandedMemoryFile = null;
    expandedInboxMsg = null;
    render();
  }
});

// ---------------------------------------------------------------------------
// Bootstrap
// ---------------------------------------------------------------------------

render(); // Show loading state immediately
fetchCache();
setInterval(fetchCache, 5000);
startTicker();
