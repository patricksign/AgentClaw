// ─── CONSTANTS ────────────────────────────────────────────────────────────────
// Default pricing — overwritten by /api/pricing on load.
const MODELS = {
    opus: { name: 'Claude Opus 4.6', color: '#a78bfa', inputPer1M: 5.00, outputPer1M: 25.00 },
    sonnet: { name: 'Claude Sonnet 4.6', color: '#60a5fa', inputPer1M: 3.00, outputPer1M: 15.00 },
    haiku: { name: 'Claude Haiku 4.5', color: '#f472b6', inputPer1M: 1.00, outputPer1M: 5.00 },
    minimax: { name: 'MiniMax M2.5', color: '#34d399', inputPer1M: 0.30, outputPer1M: 1.20 },
    glm: { name: 'GLM-4.5-Flash', color: '#fb923c', inputPer1M: 0.00, outputPer1M: 0.00 },
    'glm-flash': { name: 'GLM-4.5-Flash', color: '#fb923c', inputPer1M: 0.00, outputPer1M: 0.00 },
    glm5: { name: 'GLM-5', color: '#fbbf24', inputPer1M: 1.00, outputPer1M: 3.20 },
};

// Sync pricing from backend on page load.
fetch('/api/pricing').then(r => r.json()).then(entries => {
    for (const e of entries) {
        if (MODELS[e.alias]) {
            MODELS[e.alias].inputPer1M = e.input_per_1m;
            MODELS[e.alias].outputPer1M = e.output_per_1m;
        }
    }
}).catch(() => { /* use defaults */ });

const ROLES = [
    { id: 'idea', emoji: '🧠', name: 'Idea Agent', desc: 'Vision & concept' },
    { id: 'architect', emoji: '📐', name: 'Architect', desc: 'System design' },
    { id: 'breakdown', emoji: '📋', name: 'Breakdown', desc: 'Ticket planning' },
    { id: 'coding', emoji: '💻', name: 'Coding Agent', desc: 'Implement code' },
    { id: 'test', emoji: '🧪', name: 'Test Agent', desc: 'Write & run tests' },
    { id: 'review', emoji: '🔍', name: 'Review Agent', desc: 'Code review & PR' },
    { id: 'docs', emoji: '📝', name: 'Docs Agent', desc: 'Documentation' },
    { id: 'deploy', emoji: '🚀', name: 'Deploy Agent', desc: 'Deploy to dev' },
    { id: 'notify', emoji: '📣', name: 'Notify Agent', desc: 'Telegram/Slack' },
];

let AGENTS = [
    { id: 'opus-agent', name: 'Idea Agent', emoji: '🧠', model: 'opus', role: 'idea', desc: 'App vision & daily check' },
    { id: 'sonnet-agent', name: 'Planning Agent', emoji: '📋', model: 'sonnet', role: 'breakdown', desc: 'Ticket breakdown & sprint' },
    { id: 'ticket-agent', name: 'Ticket Agent', emoji: '🎫', model: 'glm', role: 'breakdown', desc: 'Create Trello cards' },
    { id: 'coding-agent', name: 'Coding Agent', emoji: '💻', model: 'minimax', role: 'coding', desc: 'Write & fix code' },
    { id: 'review-agent', name: 'Review Agent', emoji: '🔍', model: 'sonnet', role: 'review', desc: 'PR review & merge gate' },
];

// Scheduled reassignments: { agentId, newModel, newRole, scheduledFor }
let scheduledChanges = JSON.parse(localStorage.getItem('ac_scheduled') || '[]');

const COLUMNS = [
    { id: 'backlog', label: 'Backlog', color: '#5a5a78' },
    { id: 'todo', label: 'Todo', color: '#60a5fa' },
    { id: 'inprogress', label: 'In Progress', color: '#f5c842' },
    { id: 'review', label: 'In Review', color: '#f59042' },
    { id: 'done', label: 'Done', color: '#2dd4a0' },
];

// ─── TASK DATA ────────────────────────────────────────────────────────────────
let tasks = [
    {
        id: 'T-001', title: 'Define app concept & core features', agent: 'opus-agent', status: 'done', tags: ['strategy', 'planning'], inputTok: 5200, outputTok: 3100, time: '08:00', date: today(-2),
        checklist: [{ id: 'ci1', label: 'Write app vision doc', done: true }, { id: 'ci2', label: 'List core features', done: true }, { id: 'ci3', label: 'Define target users', done: true }]
    },
    {
        id: 'T-002', title: 'Break down MVP into sprint tickets', agent: 'sonnet-agent', status: 'done', tags: ['planning', 'tickets'], inputTok: 10400, outputTok: 7800, time: '08:12', date: today(-2),
        checklist: [{ id: 'ci4', label: 'Create epic breakdown', done: true }, { id: 'ci5', label: 'Estimate story points', done: true }]
    },
    {
        id: 'T-003', title: 'Game: Bắn Súng — Idea Ticket', agent: 'opus-agent', status: 'inprogress', tags: ['game', 'idea', 'trello'], inputTok: 12000, outputTok: 3400, time: '09:00', date: today(0),
        trelloCardId: 'TRELLO-001', trelloUrl: 'https://trello.com/c/example1',
        checklist: [
            { id: 'ci6', label: 'Game mechanics design', done: true, assignedAgent: 'coding-agent' },
            { id: 'ci7', label: 'Level system blueprint', done: true, assignedAgent: 'coding-agent' },
            { id: 'ci8', label: 'Enemy AI pattern', done: false, assignedAgent: 'coding-agent' },
            { id: 'ci9', label: 'Score & leaderboard', done: false, assignedAgent: 'coding-agent' },
            { id: 'ci10', label: 'Sound effects plan', done: false, assignedAgent: 'docs-agent' },
            { id: 'ci11', label: 'UI/UX wireframe', done: false, assignedAgent: 'review-agent' },
        ]
    },
    { id: 'T-004', title: 'Implement JWT authentication', agent: 'coding-agent', status: 'done', tags: ['auth', 'backend'], inputTok: 38000, outputTok: 12000, time: '09:00', date: today(-1) },
    { id: 'T-005', title: 'Review auth PR & merge', agent: 'review-agent', status: 'done', tags: ['review', 'auth'], inputTok: 14000, outputTok: 4200, time: '10:30', date: today(-1) },
    { id: 'T-006', title: 'Implement RabbitMQ event bus', agent: 'coding-agent', status: 'inprogress', tags: ['messaging', 'backend'], inputTok: 42000, outputTok: 0, time: '11:00', date: today(0) },
    { id: 'T-007', title: 'Create Redis caching layer', agent: 'coding-agent', status: 'inprogress', tags: ['redis', 'perf'], inputTok: 18000, outputTok: 0, time: '11:15', date: today(0) },
    { id: 'T-008', title: 'Review RabbitMQ PR', agent: 'review-agent', status: 'review', tags: ['review', 'messaging'], inputTok: 0, outputTok: 0, time: '—', date: today(0) },
    { id: 'T-009', title: 'Ticket: MongoDB aggregation', agent: 'ticket-agent', status: 'todo', tags: ['mongodb', 'tickets'], inputTok: 0, outputTok: 0, time: '—', date: today(0) },
    { id: 'T-010', title: 'Daily progress check', agent: 'opus-agent', status: 'todo', tags: ['strategy', 'daily'], inputTok: 0, outputTok: 0, time: '—', date: today(0) },
    { id: 'T-011', title: 'Implement MongoDB aggregation', agent: 'coding-agent', status: 'backlog', tags: ['mongodb', 'backend'], inputTok: 0, outputTok: 0, time: '—', date: today(1) },
    { id: 'T-012', title: 'Setup CI/CD pipeline', agent: 'coding-agent', status: 'backlog', tags: ['devops', 'ci'], inputTok: 0, outputTok: 0, time: '—', date: today(1) },
];
// Generate week/month history for metrics
for (let i = 3; i <= 30; i++) {
    tasks.push({
        id: `H-${String(i).padStart(3, '0')}`,
        title: `Historical task ${i}`,
        agent: ['opus-agent', 'sonnet-agent', 'coding-agent', 'review-agent'][i % 4],
        status: 'done', tags: ['history'],
        inputTok: 5000 + Math.random() * 40000 | 0, outputTok: 1000 + Math.random() * 10000 | 0,
        time: '—', date: today(-i)
    });
}

let taskCounter = 100;
let currentRange = 'today';
let reassignTarget = null;

// ─── HELPERS ─────────────────────────────────────────────────────────────────
function today(offset = 0) {
    const d = new Date(); d.setDate(d.getDate() + offset);
    return d.toISOString().slice(0, 10);
}
function fmtCost(c) { return '$' + c.toFixed(4) }
function fmtCostShort(c) { return '$' + (c < 0.01 ? c.toFixed(4) : c.toFixed(2)) }
function fmtTok(n) { return n >= 1000 ? (n / 1000).toFixed(1) + 'k' : String(n) }
function randInt(a, b) { return Math.floor(Math.random() * (b - a + 1)) + a }
function calcCost(model, i, o) { const m = MODELS[model] || MODELS.sonnet; return (i / 1e6) * m.inputPer1M + (o / 1e6) * m.outputPer1M }
function getModelForAgent(aid) { const a = AGENTS.find(x => x.id === aid); return a ? a.model : 'sonnet' }

function getTasksForRange(range) {
    const now = new Date();
    const todayStr = today(0);
    if (range === 'today') return tasks.filter(t => t.date === todayStr);
    if (range === 'week') {
        const d = new Date(now); d.setDate(d.getDate() - 7);
        return tasks.filter(t => t.date && t.date >= d.toISOString().slice(0, 10));
    }
    if (range === 'month') {
        const d = new Date(now); d.setMonth(d.getMonth() - 1);
        return tasks.filter(t => t.date && t.date >= d.toISOString().slice(0, 10));
    }
    if (range === 'year') {
        const d = new Date(now); d.setFullYear(d.getFullYear() - 1);
        return tasks.filter(t => t.date && t.date >= d.toISOString().slice(0, 10));
    }
    if (range === 'custom') {
        const from = document.getElementById('range-from').value;
        const to = document.getElementById('range-to').value;
        return tasks.filter(t => t.date && (!from || t.date >= from) && (!to || t.date <= to));
    }
    return tasks;
}

// ─── TRELLO INTEGRATION ───────────────────────────────────────────────────────
function setTrelloStatus(msg, type) {
    const el = document.getElementById('trello-status');
    el.textContent = msg; el.className = 'trello-status ' + type;
}

async function loadTrelloBoards() {
    const key = document.getElementById('trello-key').value.trim();
    const token = document.getElementById('trello-token').value.trim();
    if (!key || !token) { setTrelloStatus('Missing Key/Token', 'err'); return }
    setTrelloStatus('Connecting...', 'loading');
    try {
        const r = await fetch(`https://api.trello.com/1/members/me/boards?key=${key}&token=${token}&fields=id,name`);
        if (!r.ok) throw new Error('Auth failed');
        const boards = await r.json();
        const sel = document.getElementById('trello-board');
        sel.innerHTML = '<option value="">— Select Board —</option>' +
            boards.map(b => `<option value="${b.id}">${b.name}</option>`).join('');
        setTrelloStatus('Connected ✓', 'ok');
    } catch (e) { setTrelloStatus('Connection failed', 'err') }
}

async function loadTrelloCards() {
    const key = document.getElementById('trello-key').value.trim();
    const token = document.getElementById('trello-token').value.trim();
    const boardId = document.getElementById('trello-board').value;
    if (!key || !token || !boardId) { setTrelloStatus('Select a board first', 'err'); return }
    setTrelloStatus('Syncing...', 'loading');
    try {
        const r = await fetch(`https://api.trello.com/1/boards/${boardId}/cards?key=${key}&token=${token}&checklists=all&fields=id,name,shortUrl,idList,labels,desc`);
        if (!r.ok) throw new Error('Failed');
        const cards = await r.json();
        // Map trello cards to tasks
        cards.forEach(card => {
            const existing = tasks.find(t => t.trelloCardId === card.id);
            if (!existing) {
                const newTask = {
                    id: 'TR-' + card.id.slice(-6),
                    title: card.name,
                    agent: 'sonnet-agent',
                    status: 'todo',
                    tags: card.labels.map(l => l.name || l.color).filter(Boolean),
                    inputTok: 0, outputTok: 0, time: '—',
                    date: today(0),
                    trelloCardId: card.id,
                    trelloUrl: card.shortUrl,
                    checklist: card.checklists?.[0]?.checkItems?.map(ci => ({
                        id: ci.id, label: ci.name,
                        done: ci.state === 'complete',
                        assignedAgent: 'coding-agent'
                    })) || []
                };
                tasks.push(newTask);
            } else {
                // Update checklist from Trello
                if (card.checklists?.[0]?.checkItems) {
                    existing.checklist = card.checklists[0].checkItems.map(ci => ({
                        id: ci.id, label: ci.name,
                        done: ci.state === 'complete',
                        assignedAgent: existing.checklist?.find(x => x.id === ci.id)?.assignedAgent || 'coding-agent'
                    }));
                }
            }
        });
        setTrelloStatus(`Synced ${cards.length} cards ✓`, 'ok');
        renderAll();
    } catch (e) { setTrelloStatus('Sync failed', 'err') }
}

// ─── RANGE & METRICS ─────────────────────────────────────────────────────────
function setRange(range, btn) {
    currentRange = range;
    document.querySelectorAll('.metrics-bar .range-btn').forEach(b => b.classList.remove('active'));
    if (btn) btn.classList.add('active');
    updatePeriodStats();
}

function updatePeriodStats() {
    const rt = getTasksForRange(currentRange);
    const cost = rt.reduce((s, t) => s + calcCost(getModelForAgent(t.agent), t.inputTok, t.outputTok), 0);
    const tok = rt.reduce((s, t) => s + t.inputTok + t.outputTok, 0);
    document.getElementById('period-cost').textContent = fmtCostShort(cost);
    document.getElementById('period-tasks').textContent = rt.length;
    document.getElementById('period-tokens').textContent = fmtTok(tok);
}

function setMetricsRange(range, btn) {
    document.querySelectorAll('#metrics-modal .range-btn').forEach(b => b.classList.remove('active'));
    if (btn) btn.classList.add('active');
    renderMetricsChart(range);
}

function showMetricsModal() {
    renderMetricsChart('week');
    document.getElementById('metrics-modal').classList.add('open');
}

function renderMetricsChart(range) {
    const days = range === 'week' ? 7 : range === 'month' ? 30 : 365;
    const title = range === 'week' ? 'Last 7 Days' : range === 'month' ? 'Last 30 Days' : 'Last 12 Months';
    document.getElementById('metrics-modal-title').textContent = `📊 Metrics — ${title}`;

    let rows = [];
    if (range === 'year') {
        // Group by month
        for (let i = 11; i >= 0; i--) {
            const d = new Date(); d.setMonth(d.getMonth() - i);
            const key = d.toISOString().slice(0, 7); // YYYY-MM
            const mt = tasks.filter(t => t.date && t.date.startsWith(key));
            const cost = mt.reduce((s, t) => s + calcCost(getModelForAgent(t.agent), t.inputTok, t.outputTok), 0);
            const tok = mt.reduce((s, t) => s + t.inputTok + t.outputTok, 0);
            rows.push({ label: d.toLocaleDateString('en', { month: 'short', year: '2-digit' }), cost, tok, count: mt.length });
        }
    } else {
        for (let i = days - 1; i >= 0; i--) {
            const d = today(-i);
            const dt = tasks.filter(t => t.date === d);
            const cost = dt.reduce((s, t) => s + calcCost(getModelForAgent(t.agent), t.inputTok, t.outputTok), 0);
            const tok = dt.reduce((s, t) => s + t.inputTok + t.outputTok, 0);
            const label = new Date(d).toLocaleDateString('en', { month: 'short', day: 'numeric' });
            rows.push({ label, cost, tok, count: dt.length });
        }
    }

    const maxCost = Math.max(...rows.map(r => r.cost), 0.0001);
    const totalCost = rows.reduce((s, r) => s + r.cost, 0);
    const totalTok = rows.reduce((s, r) => s + r.tok, 0);
    const totalTasks = rows.reduce((s, r) => s + r.count, 0);

    document.getElementById('metrics-modal-content').innerHTML = `
    <div style="display:grid;grid-template-columns:repeat(3,1fr);gap:10px;margin-bottom:16px">
      <div style="padding:12px;background:var(--surface2);border:1px solid var(--border);border-radius:8px;text-align:center">
        <div style="font-size:9px;color:var(--text3);text-transform:uppercase;letter-spacing:1px;margin-bottom:4px">Total Cost</div>
        <div style="font-family:'Syne',sans-serif;font-size:20px;font-weight:700;color:var(--yellow)">${fmtCostShort(totalCost)}</div>
      </div>
      <div style="padding:12px;background:var(--surface2);border:1px solid var(--border);border-radius:8px;text-align:center">
        <div style="font-size:9px;color:var(--text3);text-transform:uppercase;letter-spacing:1px;margin-bottom:4px">Total Tokens</div>
        <div style="font-family:'Syne',sans-serif;font-size:20px;font-weight:700;color:var(--blue)">${fmtTok(totalTok)}</div>
      </div>
      <div style="padding:12px;background:var(--surface2);border:1px solid var(--border);border-radius:8px;text-align:center">
        <div style="font-size:9px;color:var(--text3);text-transform:uppercase;letter-spacing:1px;margin-bottom:4px">Tasks Run</div>
        <div style="font-family:'Syne',sans-serif;font-size:20px;font-weight:700;color:var(--green)">${totalTasks}</div>
      </div>
    </div>
    <div class="metrics-chart">
      ${rows.map(r => `
        <div class="metrics-row">
          <div class="metrics-date">${r.label}</div>
          <div class="metrics-bar-mini"><div class="metrics-bar-fill" style="width:${(r.cost / maxCost * 100).toFixed(1)}%"></div></div>
          <div class="metrics-cost">${fmtCostShort(r.cost)}</div>
          <div class="metrics-tasks">${r.count} tasks</div>
        </div>`).join('')}
    </div>`;
}

// ─── STATS ───────────────────────────────────────────────────────────────────
function computeStats() {
    const done = tasks.filter(t => t.status === 'done');
    const totalCost = tasks.reduce((s, t) => s + calcCost(getModelForAgent(t.agent), t.inputTok, t.outputTok), 0);
    const totalTok = tasks.reduce((s, t) => s + t.inputTok + t.outputTok, 0);
    const pct = tasks.length ? Math.round((done.length / tasks.length) * 100) : 0;
    const agentCosts = {};
    tasks.forEach(t => { const c = calcCost(getModelForAgent(t.agent), t.inputTok, t.outputTok); agentCosts[t.agent] = (agentCosts[t.agent] || 0) + c });
    const topAgent = Object.entries(agentCosts).sort((a, b) => b[1] - a[1])[0];
    const topAgentObj = topAgent ? AGENTS.find(a => a.id === topAgent[0]) : null;
    const trelloTasks = tasks.filter(t => t.trelloCardId);
    const trelloWithChecklist = trelloTasks.filter(t => t.checklist && t.checklist.length > 0);
    return { done, totalCost, totalTok, pct, topAgent, topAgentObj, agentCosts, trelloTasks, trelloWithChecklist };
}

// ─── RENDER ───────────────────────────────────────────────────────────────────
function renderHeader() {
    const { totalCost, totalTok } = computeStats();
    const now = new Date();
    document.getElementById('date-badge').textContent = now.toLocaleDateString('en-US', { weekday: 'short', month: 'short', day: 'numeric', year: 'numeric' });
    document.getElementById('today-cost').textContent = 'Today: ' + fmtCostShort(totalCost);
    document.getElementById('today-tokens').textContent = fmtTok(totalTok) + ' tokens';
}

function renderSummary() {
    const { done, totalCost, totalTok, pct, topAgentObj, topAgent, trelloTasks, trelloWithChecklist } = computeStats();
    document.getElementById('sum-tasks').textContent = tasks.length;
    document.getElementById('sum-tasks-sub').textContent = done.length + ' done';
    document.getElementById('sum-trello').textContent = trelloTasks.length;
    document.getElementById('sum-trello-sub').textContent = trelloWithChecklist.length + ' with checklist';
    document.getElementById('sum-tokens').textContent = fmtTok(totalTok);
    document.getElementById('sum-cost').textContent = fmtCostShort(totalCost);
    document.getElementById('sum-cost-sub').textContent = 'est. monthly: $' + (totalCost * 30).toFixed(0);
    if (topAgentObj) { const m = MODELS[topAgentObj.model]; document.getElementById('sum-expensive').textContent = topAgentObj.emoji + ' ' + topAgentObj.name; document.getElementById('sum-expensive').style.color = m.color; document.getElementById('sum-exp-cost').textContent = fmtCost(topAgent[1]) }
    document.getElementById('sum-pct').textContent = pct + '%';
    document.getElementById('sum-pct-sub').textContent = done.length + ' of ' + tasks.length + ' done';
}

function renderAgentList() {
    const { agentCosts } = computeStats();
    const el = document.getElementById('agent-list');
    el.innerHTML = AGENTS.map(a => {
        const m = MODELS[a.model];
        const cost = agentCosts[a.id] || 0;
        const tok = tasks.filter(t => t.agent === a.id).reduce((s, t) => s + t.inputTok + t.outputTok, 0);
        const scheduled = scheduledChanges.find(sc => sc.agentId === a.id);
        const agentTasks = tasks.filter(t => t.agent === a.id);
        const doneTasks = agentTasks.filter(t => t.status === 'done').length;
        const runningTasks = agentTasks.filter(t => t.status === 'inprogress').length;
        // Status indicator dot
        const statusColor = runningTasks > 0 ? 'var(--green)' : doneTasks > 0 ? 'var(--blue)' : 'var(--border2)';
        return `<div class="agent-row">
      <!-- Left: clickable area → agent detail -->
      <div style="display:flex;align-items:center;gap:8px;flex:1;min-width:0;cursor:pointer" onclick="showAgentDetail('${a.id}')">
        <div style="position:relative">
          <div class="agent-avatar" style="background:${m.color}22;border:1px solid ${m.color}44">${a.emoji}</div>
          <div style="position:absolute;bottom:-1px;right:-1px;width:7px;height:7px;border-radius:50%;background:${statusColor};border:1.5px solid var(--surface)"></div>
        </div>
        <div class="agent-info">
          <div class="agent-name">${a.name}${scheduled ? `<span class="scheduled-badge">→${scheduled.newRole}</span>` : ''}</div>
          <div class="agent-model-tag">${m.name} · ${doneTasks} done${runningTasks ? ` · <span style="color:var(--green)">${runningTasks} running</span>` : ''}</div>
        </div>
        <div class="agent-nums"><div class="v" style="color:${m.color}">${fmtTok(tok)}</div><div class="c">${fmtCost(cost)}</div></div>
      </div>
      <!-- Right: reassign button -->
      <button class="reassign-btn" style="opacity:1;position:static;transform:none;margin-left:6px" onclick="showReassign('${a.id}')">⇄</button>
    </div>`;
    }).join('');
}

function renderScheduled() {
    const el = document.getElementById('scheduled-list');
    if (!scheduledChanges.length) { el.textContent = 'No changes scheduled'; return }
    el.innerHTML = scheduledChanges.map(sc => {
        const a = AGENTS.find(x => x.id === sc.agentId);
        const m = MODELS[sc.newModel];
        return `<div style="padding:8px;background:var(--surface2);border:1px solid var(--border);border-radius:6px;margin-bottom:6px">
      <div style="font-size:10px;color:var(--text);margin-bottom:3px">${a ? a.emoji + ' ' + a.name : 'Unknown'}</div>
      <div style="font-size:9px;color:var(--text3)">→ ${sc.newRole} · <span style="color:${m ? m.color : '#fff'}">${sc.newModel}</span></div>
      <div style="font-size:8px;color:var(--accent);margin-top:2px">Applies: ${sc.scheduledFor}</div>
    </div>`;
    }).join('');
}

function renderModelUsage() {
    const modelTok = {}, modelCost = {};
    tasks.forEach(t => { const model = getModelForAgent(t.agent); modelTok[model] = (modelTok[model] || 0) + t.inputTok + t.outputTok; modelCost[model] = (modelCost[model] || 0) + calcCost(model, t.inputTok, t.outputTok) });
    document.getElementById('model-usage').innerHTML = Object.entries(modelTok).sort((a, b) => b[1] - a[1]).map(([model, tok]) => {
        const m = MODELS[model];
        return `<div style="margin-bottom:8px;padding:8px 10px;background:var(--surface2);border:1px solid var(--border);border-radius:7px">
      <div style="display:flex;justify-content:space-between;margin-bottom:4px">
        <span style="font-size:10px;color:${m.color};font-weight:500">${m.name}</span>
        <span style="font-size:9px;color:var(--yellow)">${fmtCost(modelCost[model])}</span>
      </div>
      <div style="font-size:9px;color:var(--text3)">${fmtTok(tok)} tokens</div>
    </div>`;
    }).join('');
}

function renderKanban() {
    document.getElementById('kanban-board').innerHTML = COLUMNS.map(col => {
        const ct = tasks.filter(t => t.status === col.id);
        return `<div class="column">
      <div class="column-header">
        <div class="column-title"><div class="col-dot" style="background:${col.color}"></div>${col.label}</div>
        <div class="column-count">${ct.length}</div>
      </div>
      <div class="column-body">${ct.length ? ct.map(renderTaskCard).join('') : '<div class="empty-col">No tasks</div>'}</div>
    </div>`;
    }).join('');
}

function renderTaskCard(t) {
    const agent = AGENTS.find(a => a.id === t.agent);
    const model = getModelForAgent(t.agent);
    const m = MODELS[model];
    const cost = calcCost(model, t.inputTok, t.outputTok);
    const totalTok = t.inputTok + t.outputTok;
    const tagColors = ['#7c6af733', '#34d39933', '#60a5fa33', '#f5c84233', '#f5904233'];
    const tags = (t.tags || []).map((tag, i) => `<span class="tag" style="background:${tagColors[i % tagColors.length]};color:var(--text2)">${tag}</span>`).join('');
    const trelloBadge = t.trelloCardId ? `<a href="${t.trelloUrl || '#'}" target="_blank" onclick="event.stopPropagation()" style="font-size:8px;color:#4da6ff;padding:1px 6px;background:#0052cc22;border:1px solid #0052cc44;border-radius:4px;text-decoration:none">Trello ↗</a>` : '';

    // Checklist preview
    let checklistHtml = '';
    if (t.checklist && t.checklist.length) {
        const done = t.checklist.filter(ci => ci.done).length;
        const total = t.checklist.length;
        const pct = Math.round(done / total * 100);
        const preview = t.checklist.slice(0, 3);
        checklistHtml = `<div class="checklist-bar">
      <div class="checklist-header"><span>AgentClaw Tasks (${done}/${total})</span><span>${pct}%</span></div>
      <div class="checklist-progress"><div class="checklist-fill" style="width:${pct}%"></div></div>
      <div class="checklist-items">
        ${preview.map(ci => `<div class="checklist-item ${ci.done ? 'done' : ''}">
          <div class="checklist-check ${ci.done ? 'done' : ''}">${ci.done ? '✓' : ''}</div>
          <span>${ci.label}</span>
        </div>`).join('')}
        ${total > 3 ? `<div style="font-size:8px;color:var(--text3);padding:2px 0 0 15px">+${total - 3} more...</div>` : ''}
      </div>
    </div>`;
    }

    return `<div class="task-card" onclick="showDetail('${t.id}')">
    <div style="position:absolute;left:0;top:0;bottom:0;width:3px;background:${m.color};border-radius:3px 0 0 3px"></div>
    <div style="padding-left:4px">
      <div class="task-id">${t.id} · ${t.time} ${trelloBadge}</div>
      <div class="task-title">${t.title}</div>
      <div class="task-tags">${tags}</div>
      <div class="task-meta">
        <div class="task-agent"><div class="task-agent-dot" style="background:${m.color}"></div>${agent ? agent.emoji + ' ' + agent.name : ''}</div>
        ${totalTok ? `<div class="token-badge"><span class="tok">${fmtTok(totalTok)}</span><span style="color:var(--text3)">·</span><span class="cost">${fmtCost(cost)}</span></div>` : `<div class="token-badge"><span class="tok" style="color:var(--text3)">pending</span></div>`}
      </div>
      ${checklistHtml}
    </div>
  </div>`;
}

function renderTokenBar() {
    const modelTok = {};
    tasks.forEach(t => { const model = getModelForAgent(t.agent); modelTok[model] = (modelTok[model] || 0) + t.inputTok + t.outputTok });
    const total = Object.values(modelTok).reduce((a, b) => a + b, 0) || 1;
    const sorted = Object.entries(modelTok).sort((a, b) => b[1] - a[1]);
    document.getElementById('bar-track').innerHTML = sorted.map(([model, tok]) => { const m = MODELS[model]; return `<div class="bar-segment" style="width:${(tok / total * 100).toFixed(1)}%;background:${m.color}"></div>` }).join('');
    document.getElementById('bar-legend').innerHTML = sorted.map(([model]) => { const m = MODELS[model]; const label = m.name.split(' ').slice(-1)[0]; return `<div class="legend-item"><div class="legend-dot" style="background:${m.color}"></div>${label}</div>` }).join('');
}

function renderAll() {
    renderHeader();
    renderSummary();
    renderAgentList();
    renderScheduled();
    renderModelUsage();
    renderKanban();
    renderTokenBar();
    updatePeriodStats();
}

// ─── DETAIL MODAL ─────────────────────────────────────────────────────────────
function showDetail(taskId) {
    const t = tasks.find(x => x.id === taskId);
    if (!t) return;
    const agent = AGENTS.find(a => a.id === t.agent);
    const model = getModelForAgent(t.agent);
    const m = MODELS[model];
    const cost = calcCost(model, t.inputTok, t.outputTok);
    const col = COLUMNS.find(c => c.id === t.status);
    document.getElementById('modal-task-title').textContent = t.title;

    let checklistSection = '';
    if (t.checklist && t.checklist.length) {
        const done = t.checklist.filter(ci => ci.done).length;
        checklistSection = `
      <div style="margin-top:16px">
        <div style="font-size:9px;text-transform:uppercase;letter-spacing:1.5px;color:var(--text3);margin-bottom:10px">AgentClaw Tasks Checklist (${done}/${t.checklist.length})</div>
        ${t.checklist.map(ci => {
            const ca = AGENTS.find(a => a.id === ci.assignedAgent);
            return `<div class="checklist-modal-item ${ci.done ? 'done' : ''}">
            <div class="ci-check ${ci.done ? 'done' : ''}" onclick="toggleCheckItem('${t.id}','${ci.id}')">${ci.done ? '✓' : ''}</div>
            <span class="ci-label ${ci.done ? 'done' : ''}">${ci.label}</span>
            <span class="ci-agent">${ca ? ca.emoji + ' ' + ca.name : 'unassigned'}</span>
          </div>`;
        }).join('')}
      </div>`;
    }

    document.getElementById('modal-content').innerHTML = `
    <div class="detail-row"><span class="detail-label">Task ID</span><span class="detail-value">${t.id}</span></div>
    <div class="detail-row"><span class="detail-label">Status</span><span class="detail-value" style="color:${col.color}">${col.label}</span></div>
    <div class="detail-row"><span class="detail-label">Agent</span><span class="detail-value">${agent ? agent.emoji + ' ' + agent.name : '—'}</span></div>
    <div class="detail-row"><span class="detail-label">Model</span><span class="detail-value" style="color:${m.color}">${m.name}</span></div>
    <div class="detail-row"><span class="detail-label">Started</span><span class="detail-value">${t.time}</span></div>
    ${t.trelloCardId ? `<div class="detail-row"><span class="detail-label">Trello Card</span><a href="${t.trelloUrl || '#'}" target="_blank" class="detail-value" style="color:#4da6ff">${t.trelloCardId} ↗</a></div>` : ''}
    <div class="token-breakdown">
      <div class="token-breakdown-title">Token Usage & Cost</div>
      <div class="token-row"><span class="label">Input tokens</span><span class="val">${t.inputTok.toLocaleString()}</span></div>
      <div class="token-row"><span class="label">Input cost</span><span class="val">${fmtCost((t.inputTok / 1e6) * m.inputPer1M)}</span></div>
      <div class="token-row"><span class="label">Output tokens</span><span class="val">${t.outputTok.toLocaleString()}</span></div>
      <div class="token-row"><span class="label">Output cost</span><span class="val">${fmtCost((t.outputTok / 1e6) * m.outputPer1M)}</span></div>
      <div class="token-row total"><span class="label">Total cost</span><span class="val">${fmtCost(cost)}</span></div>
    </div>
    ${checklistSection}
    <div style="display:flex;gap:8px;margin-top:16px;justify-content:flex-end">
      ${t.status !== 'done' ? `<button class="btn btn-primary" onclick="markDone('${t.id}')">✓ Mark Done</button>` : ''}
      <button class="btn" onclick="simulateRun('${t.id}')">▶ Simulate</button>
    </div>`;
    document.getElementById('detail-modal').classList.add('open');
}

function toggleCheckItem(taskId, ciId) {
    const t = tasks.find(x => x.id === taskId);
    if (!t || !t.checklist) return;
    const ci = t.checklist.find(x => x.id === ciId);
    if (ci) ci.done = !ci.done;
    showDetail(taskId);
    renderKanban();
}

// ─── REASSIGN ─────────────────────────────────────────────────────────────────
function showReassign(agentId) {
    const a = AGENTS.find(x => x.id === agentId);
    if (!a) return;
    reassignTarget = agentId;
    document.getElementById('reassign-agent-name').textContent = a.emoji + ' ' + a.name;
    document.getElementById('reassign-current-model').textContent = MODELS[a.model]?.name || a.model;

    // Role grid
    document.getElementById('reassign-role-grid').innerHTML = ROLES.map(r => `
    <div class="role-option ${a.role === r.id ? 'selected' : ''}" onclick="selectRole('${r.id}',this)">
      <div class="role-emoji">${r.emoji}</div>
      <div class="role-name">${r.name}</div>
      <div class="role-model">${r.desc}</div>
    </div>`).join('');

    // Model select
    const mSel = document.getElementById('reassign-model');
    mSel.innerHTML = Object.entries(MODELS).map(([k, v]) => `<option value="${k}" ${a.model === k ? 'selected' : ''}>${v.name}</option>`).join('');

    document.getElementById('reassign-modal').classList.add('open');
}

function selectRole(roleId, el) {
    document.querySelectorAll('.role-option').forEach(x => x.classList.remove('selected'));
    el.classList.add('selected');
}

function confirmReassign() {
    const a = AGENTS.find(x => x.id === reassignTarget);
    if (!a) return;
    const selectedRole = document.querySelector('.role-option.selected');
    const newRole = selectedRole ? ROLES.find(r => r.name === selectedRole.querySelector('.role-name').textContent)?.id || a.role : a.role;
    const newModel = document.getElementById('reassign-model').value;
    const scheduledFor = today(1);

    // Remove existing schedule for this agent
    scheduledChanges = scheduledChanges.filter(sc => sc.agentId !== reassignTarget);
    scheduledChanges.push({ agentId: reassignTarget, newModel, newRole, scheduledFor });
    localStorage.setItem('ac_scheduled', JSON.stringify(scheduledChanges));

    closeModal('reassign-modal');
    renderAll();

    // Show confirmation
    const a2 = AGENTS.find(x => x.id === reassignTarget);
    alert(`✅ Scheduled: ${a2?.name} will switch to ${newRole} using ${MODELS[newModel]?.name} starting tomorrow (${scheduledFor})`);
}

// Apply scheduled changes (run at start of new day)
function applyScheduledChanges() {
    const todayStr = today(0);
    const toApply = scheduledChanges.filter(sc => sc.scheduledFor <= todayStr);
    toApply.forEach(sc => {
        const a = AGENTS.find(x => x.id === sc.agentId);
        if (a) { a.model = sc.newModel; a.role = sc.newRole }
    });
    scheduledChanges = scheduledChanges.filter(sc => sc.scheduledFor > todayStr);
    localStorage.setItem('ac_scheduled', JSON.stringify(scheduledChanges));
}

// ─── INTERACTIONS ─────────────────────────────────────────────────────────────
let activeFilter = null;
function filterAgent(agentId) {
    if (activeFilter === agentId) { activeFilter = null; document.querySelectorAll('.agent-row').forEach(c => c.classList.remove('active')); document.getElementById('board-title').textContent = '📋 Sprint Board'; renderAll(); return }
    activeFilter = agentId;
    document.querySelectorAll('.agent-row').forEach(c => c.classList.remove('active'));
    const agent = AGENTS.find(a => a.id === agentId);
    document.getElementById('board-title').textContent = `${agent.emoji} ${agent.name} Tasks`;
    const orig = tasks; const filtered = tasks.filter(t => t.agent === agentId);
    tasks = filtered; renderKanban(); renderTokenBar(); tasks = orig;
}

// ─── AGENT DETAIL ────────────────────────────────────────────────────────────
function showAgentDetail(agentId) {
    const a = AGENTS.find(x => x.id === agentId);
    if (!a) return;
    const m = MODELS[a.model];
    const todayStr = today(0);
    const agentTasks = tasks.filter(t => t.agent === agentId);
    const todayTasks = agentTasks.filter(t => t.date === todayStr);
    const allDone = agentTasks.filter(t => t.status === 'done');
    const totalTok = agentTasks.reduce((s, t) => s + t.inputTok + t.outputTok, 0);
    const totalCost = agentTasks.reduce((s, t) => s + calcCost(a.model, t.inputTok, t.outputTok), 0);
    const todayTok = todayTasks.reduce((s, t) => s + t.inputTok + t.outputTok, 0);
    const todayCost = todayTasks.reduce((s, t) => s + calcCost(a.model, t.inputTok, t.outputTok), 0);
    const scheduled = scheduledChanges.find(sc => sc.agentId === agentId);

    // Avg tokens per task
    const avgTok = agentTasks.length ? Math.round(totalTok / agentTasks.length) : 0;

    // Build hourly activity for today (0-23)
    const hourly = Array(24).fill(0);
    todayTasks.forEach(t => {
        if (t.time && t.time !== '—') {
            const h = parseInt(t.time.split(':')[0]);
            if (!isNaN(h)) hourly[h] += (t.inputTok + t.outputTok) || 1000;
        }
    });
    const maxH = Math.max(...hourly, 1);

    // Token donut: input vs output
    const totalIn = agentTasks.reduce((s, t) => s + t.inputTok, 0);
    const totalOut = agentTasks.reduce((s, t) => s + t.outputTok, 0);
    const donutTotal = totalIn + totalOut || 1;
    const inPct = totalIn / donutTotal;
    const outPct = totalOut / donutTotal;
    const r = 28, circ = 2 * Math.PI * r;
    const inDash = (inPct * circ).toFixed(1);
    const outDash = (outPct * circ).toFixed(1);

    // Sort today tasks by time
    const sortedToday = [...todayTasks].sort((a, b) => {
        if (a.time === '—') return 1; if (b.time === '—') return -1;
        return a.time.localeCompare(b.time);
    });

    // Estimate duration: simulate ~2min per 1k tokens
    function estDuration(t) {
        const tok = t.inputTok + t.outputTok;
        if (!tok) return '—';
        const mins = Math.round(tok / 1000 * 2);
        return mins < 60 ? `${mins}m` : `${Math.floor(mins / 60)}h${mins % 60 ? ` ${mins % 60}m` : ''}`;
    }

    document.getElementById('agent-detail-title').textContent = `${a.emoji} ${a.name} — Today`;

    document.getElementById('agent-detail-content').innerHTML = `
    <!-- Agent header -->
    <div class="agent-detail-header">
      <div class="agent-detail-avatar" style="background:${m.color}22;border:2px solid ${m.color}55">${a.emoji}</div>
      <div style="flex:1">
        <div class="agent-detail-name">${a.name}</div>
        <div class="agent-detail-meta">
          Role: <span style="color:${m.color}">${a.role}</span> ·
          Model: <span style="color:${m.color}">${m.name}</span> ·
          ID: <span style="color:var(--text3)">${a.id}</span>
        </div>
        ${scheduled ? `<div style="font-size:9px;margin-top:4px;color:var(--accent)">⏰ Scheduled: → ${scheduled.newRole} using ${scheduled.newModel} on ${scheduled.scheduledFor}</div>` : ''}
      </div>
      <div style="text-align:right">
        <div style="font-size:9px;color:var(--text3);margin-bottom:2px">Today's cost</div>
        <div style="font-family:'Syne',sans-serif;font-size:20px;font-weight:700;color:var(--yellow)">${fmtCostShort(todayCost)}</div>
        <div style="font-size:9px;color:var(--text3);margin-top:2px">${todayTasks.length} tasks today</div>
      </div>
    </div>

    <!-- Stat grid -->
    <div class="agent-stat-grid">
      <div class="agent-stat">
        <div class="agent-stat-label">Total Tasks</div>
        <div class="agent-stat-val" style="color:var(--blue)">${agentTasks.length}</div>
      </div>
      <div class="agent-stat">
        <div class="agent-stat-label">Completed</div>
        <div class="agent-stat-val" style="color:var(--green)">${allDone.length}</div>
      </div>
      <div class="agent-stat">
        <div class="agent-stat-label">Total Tokens</div>
        <div class="agent-stat-val" style="color:var(--blue)">${fmtTok(totalTok)}</div>
      </div>
      <div class="agent-stat">
        <div class="agent-stat-label">Avg / Task</div>
        <div class="agent-stat-val" style="color:var(--text)">${fmtTok(avgTok)}</div>
      </div>
      <div class="agent-stat">
        <div class="agent-stat-label">Total Cost</div>
        <div class="agent-stat-val" style="color:var(--yellow);font-size:13px">${fmtCostShort(totalCost)}</div>
      </div>
      <div class="agent-stat">
        <div class="agent-stat-label">Today Tokens</div>
        <div class="agent-stat-val" style="color:var(--blue)">${fmtTok(todayTok)}</div>
      </div>
      <div class="agent-stat">
        <div class="agent-stat-label">Input / Output</div>
        <div class="agent-stat-val" style="color:var(--text);font-size:12px">${fmtTok(totalIn)} / ${fmtTok(totalOut)}</div>
      </div>
      <div class="agent-stat">
        <div class="agent-stat-label">Success Rate</div>
        <div class="agent-stat-val" style="color:var(--green)">${agentTasks.length ? Math.round(allDone.length / agentTasks.length * 100) : 0}%</div>
      </div>
    </div>

    <!-- Hourly activity chart -->
    <div style="background:var(--surface2);border:1px solid var(--border);border-radius:8px;padding:12px;margin-bottom:14px">
      <div style="font-size:9px;text-transform:uppercase;letter-spacing:1.5px;color:var(--text3);margin-bottom:8px">Today's Activity (by hour)</div>
      <div class="hourly-bar">
        ${hourly.map((v, h) => `<div class="h-bar" style="height:${Math.max(4, (v / maxH * 100)).toFixed(0)}%;background:${v > 0 ? m.color + '99' : 'var(--border)'}" data-tip="${h}:00 — ${fmtTok(v)} tok"></div>`).join('')}
      </div>
      <div style="display:flex;justify-content:space-between;font-size:8px;color:var(--text3);margin-top:4px;padding:0 2px">
        <span>00:00</span><span>06:00</span><span>12:00</span><span>18:00</span><span>23:00</span>
      </div>
    </div>

    <!-- Token donut + breakdown -->
    <div style="display:flex;gap:12px;margin-bottom:14px">
      <div style="background:var(--surface2);border:1px solid var(--border);border-radius:8px;padding:12px;display:flex;align-items:center;gap:14px;flex:1">
        <div class="token-donut">
          <svg width="80" height="80" viewBox="0 0 80 80">
            <circle cx="40" cy="40" r="${r}" fill="none" stroke="var(--border2)" stroke-width="10"/>
            <circle cx="40" cy="40" r="${r}" fill="none" stroke="${m.color}" stroke-width="10"
              stroke-dasharray="${inDash} ${circ}" stroke-linecap="round"/>
            <circle cx="40" cy="40" r="${r}" fill="none" stroke="${m.color}55" stroke-width="10"
              stroke-dasharray="${outDash} ${circ}" stroke-dashoffset="${-inDash}"  stroke-linecap="round"/>
          </svg>
          <div class="token-donut-label"><strong>${fmtTok(donutTotal)}</strong>total</div>
        </div>
        <div style="flex:1">
          <div style="font-size:9px;text-transform:uppercase;letter-spacing:1px;color:var(--text3);margin-bottom:8px">Token Breakdown</div>
          <div style="display:flex;justify-content:space-between;font-size:10px;padding:5px 0;border-bottom:1px solid var(--border)">
            <span style="color:${m.color}">■ Input</span>
            <span>${fmtTok(totalIn)} <span style="color:var(--text3)">(${(inPct * 100).toFixed(0)}%)</span></span>
            <span style="color:var(--yellow)">${fmtCost((totalIn / 1e6) * m.inputPer1M)}</span>
          </div>
          <div style="display:flex;justify-content:space-between;font-size:10px;padding:5px 0;border-bottom:1px solid var(--border)">
            <span style="color:${m.color}55">■ Output</span>
            <span>${fmtTok(totalOut)} <span style="color:var(--text3)">(${(outPct * 100).toFixed(0)}%)</span></span>
            <span style="color:var(--yellow)">${fmtCost((totalOut / 1e6) * m.outputPer1M)}</span>
          </div>
          <div style="display:flex;justify-content:space-between;font-size:10px;padding:5px 0;font-weight:600">
            <span style="color:var(--text)">Total</span>
            <span>${fmtTok(totalTok)}</span>
            <span style="color:var(--yellow)">${fmtCostShort(totalCost)}</span>
          </div>
        </div>
      </div>
    </div>

    <!-- Today's task timeline -->
    <div style="font-size:9px;text-transform:uppercase;letter-spacing:1.5px;color:var(--text3);margin-bottom:8px">
      Today's Timeline (${todayTasks.length} tasks)
    </div>
    ${sortedToday.length === 0
            ? `<div style="text-align:center;padding:24px;color:var(--text3);font-size:11px;border:1px dashed var(--border);border-radius:8px">No tasks today</div>`
            : `<div class="task-timeline">
        ${sortedToday.map((t, i) => {
                const tok = t.inputTok + t.outputTok;
                const cost = calcCost(a.model, t.inputTok, t.outputTok);
                const dur = estDuration(t);
                const col = COLUMNS.find(c => c.id === t.status);
                const pctOfDay = todayTok ? Math.round(tok / todayTok * 100) : 0;
                return `<div class="tl-item" onclick="closeModal('agent-detail-modal');showDetail('${t.id}')">
            <div class="tl-dot-col">
              <div class="tl-dot" style="background:${col ? col.color : 'var(--border2)'}"></div>
              ${i < sortedToday.length - 1 ? `<div class="tl-line"></div>` : ''}
            </div>
            <div class="tl-content">
              <div style="display:flex;align-items:flex-start;justify-content:space-between;gap:8px">
                <div class="tl-title">${t.title}</div>
                <span class="tl-status ${t.status}">${t.status}</span>
              </div>
              <div class="tl-time">
                🕐 ${t.time !== '—' ? t.time : 'pending'} &nbsp;·&nbsp;
                ⏱ ${dur} &nbsp;·&nbsp;
                📋 ${t.id}
                ${t.trelloCardId ? ` &nbsp;·&nbsp; <a href="${t.trelloUrl || '#'}" target="_blank" onclick="event.stopPropagation()" style="color:#4da6ff;font-size:9px">Trello ↗</a>` : ''}
              </div>
              <div class="tl-stats">
                <span class="tl-stat" style="color:var(--blue)">↑ ${t.inputTok.toLocaleString()} in</span>
                <span class="tl-stat" style="color:var(--blue)">↓ ${t.outputTok.toLocaleString()} out</span>
                <span class="tl-stat" style="color:var(--yellow)">${fmtCost(cost)}</span>
                ${tok ? `<span class="tl-stat" style="color:var(--text3)">${pctOfDay}% of day</span>` : ''}
                ${(t.tags || []).map(tag => `<span class="tl-stat">${tag}</span>`).join('')}
              </div>
              ${tok ? `<div style="margin-top:6px;height:3px;background:var(--surface3);border-radius:2px;overflow:hidden">
                <div style="height:100%;width:${pctOfDay}%;background:${m.color}88;border-radius:2px"></div>
              </div>`: ''}
            </div>
          </div>`;
            }).join('')}
      </div>`
        }

    <div style="display:flex;gap:8px;margin-top:16px;justify-content:flex-end">
      <button class="btn" onclick="closeModal('agent-detail-modal');showReassign('${agentId}')">⇄ Reassign</button>
      <button class="btn btn-primary" onclick="closeModal('agent-detail-modal')">Close</button>
    </div>`;

    document.getElementById('agent-detail-modal').classList.add('open');
}


function closeModal(id) {
    const el = document.getElementById(id);
    if (el) el.classList.remove('open');
}

function markDone(taskId) { const t = tasks.find(x => x.id === taskId); if (t) { t.status = 'done'; if (!t.inputTok) t.inputTok = randInt(5000, 40000); if (!t.outputTok) t.outputTok = randInt(1000, 10000) } closeModal('detail-modal'); renderAll() }

function simulateRun(taskId) {
    const t = tasks.find(x => x.id === taskId); if (!t) return;
    const ranges = { 'opus-agent': { in: [15000, 25000], out: [2000, 4000] }, 'sonnet-agent': { in: [8000, 12000], out: [5000, 9000] }, 'ticket-agent': { in: [3000, 6000], out: [1500, 3000] }, 'coding-agent': { in: [30000, 50000], out: [8000, 18000] }, 'review-agent': { in: [10000, 18000], out: [3000, 6000] } };
    const r = ranges[t.agent] || ranges['sonnet-agent'];
    t.inputTok = randInt(r.in[0], r.in[1]); t.outputTok = randInt(r.out[0], r.out[1]);
    t.status = 'inprogress'; const now = new Date();
    t.time = now.getHours().toString().padStart(2, '0') + ':' + now.getMinutes().toString().padStart(2, '0');
    t.date = today(0);
    closeModal('detail-modal'); renderAll();
    setTimeout(() => { const task = tasks.find(x => x.id === taskId); if (task) { task.status = 'done'; renderAll() } }, 1500);
}

function isTrelloConnected() {
    const key = document.getElementById('trello-key').value.trim();
    const token = document.getElementById('trello-token').value.trim();
    const boardId = document.getElementById('trello-board').value;
    return !!(key && token && boardId);
}

async function showAddTask() {
    // Populate agent dropdown
    document.getElementById('new-agent').innerHTML = AGENTS.map(a => `<option value="${a.id}">${a.emoji} ${a.name}</option>`).join('');

    // Reset fields
    document.getElementById('new-title').value = '';
    document.getElementById('new-desc').value = '';
    document.getElementById('new-tags').value = '';
    document.getElementById('add-task-status').style.display = 'none';
    document.getElementById('add-task-btn').disabled = false;
    document.getElementById('add-task-btn').textContent = '+ Add Task';

    if (isTrelloConnected()) {
        // Show Trello indicator
        document.getElementById('add-trello-indicator').style.display = 'flex';
        document.getElementById('trello-list-group').style.display = 'block';
        document.getElementById('add-modal-title').textContent = '+ New Task → Trello';

        // Load lists for the selected board
        await loadTrelloLists();
    } else {
        document.getElementById('add-trello-indicator').style.display = 'none';
        document.getElementById('trello-list-group').style.display = 'none';
        document.getElementById('add-modal-title').textContent = '+ New Task';
    }

    document.getElementById('add-modal').classList.add('open');
}

async function loadTrelloLists() {
    const key = document.getElementById('trello-key').value.trim();
    const token = document.getElementById('trello-token').value.trim();
    const boardId = document.getElementById('trello-board').value;
    const sel = document.getElementById('new-trello-list');
    sel.innerHTML = '<option value="">Loading...</option>';
    try {
        const r = await fetch(`https://api.trello.com/1/boards/${boardId}/lists?key=${key}&token=${token}&filter=open&fields=id,name`);
        if (!r.ok) throw new Error();
        const lists = await r.json();
        sel.innerHTML = lists.map(l => `<option value="${l.id}">${l.name}</option>`).join('');
    } catch {
        sel.innerHTML = '<option value="">Could not load lists</option>';
    }
}

async function addTask() {
    const title = document.getElementById('new-title').value.trim();
    if (!title) return;
    const agent = document.getElementById('new-agent').value;
    const status = document.getElementById('new-status').value;
    const desc = document.getElementById('new-desc').value.trim();
    const tags = document.getElementById('new-tags').value.split(',').map(t => t.trim()).filter(Boolean);
    const taskId = 'T-' + String(taskCounter++).padStart(3, '0');
    const now = new Date();
    const timeStr = now.getHours().toString().padStart(2, '0') + ':' + now.getMinutes().toString().padStart(2, '0');

    const newTask = {
        id: taskId, title, agent, status,
        tags: tags.length ? tags : ['manual'],
        inputTok: 0, outputTok: 0,
        time: timeStr, date: today(0),
        description: desc
    };

    const statusEl = document.getElementById('add-task-status');
    const btn = document.getElementById('add-task-btn');

    // Push to Trello if connected
    if (isTrelloConnected()) {
        const key = document.getElementById('trello-key').value.trim();
        const token = document.getElementById('trello-token').value.trim();
        const listId = document.getElementById('new-trello-list').value;

        if (!listId) {
            statusEl.style.display = 'block';
            statusEl.style.background = '#f5656510';
            statusEl.style.color = 'var(--red)';
            statusEl.style.border = '1px solid #f5656530';
            statusEl.textContent = '⚠️ Please select a Trello list';
            return;
        }

        btn.disabled = true;
        btn.textContent = 'Creating on Trello...';
        statusEl.style.display = 'block';
        statusEl.style.background = '#f5c84210';
        statusEl.style.color = 'var(--yellow)';
        statusEl.style.border = '1px solid #f5c84230';
        statusEl.textContent = '⏳ Creating card on Trello...';

        try {
            const labelIds = [];
            // Build desc with AgentClaw metadata
            const cardDesc = `${desc}\n\n---\n*Created by AgentClaw*\n*Agent: ${AGENTS.find(a => a.id === agent)?.name || agent}*\n*Tags: ${tags.join(', ')}*`;

            const r = await fetch(`https://api.trello.com/1/cards?key=${key}&token=${token}`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    name: title,
                    desc: cardDesc,
                    idList: listId,
                    pos: 'top'
                })
            });

            if (!r.ok) throw new Error('Trello API error ' + r.status);
            const card = await r.json();

            // Link card to local task
            newTask.trelloCardId = card.id;
            newTask.trelloUrl = card.shortUrl;

            statusEl.style.background = '#22d3a010';
            statusEl.style.color = 'var(--green)';
            statusEl.style.border = '1px solid #22d3a030';
            statusEl.textContent = `✓ Created on Trello — ${card.shortUrl}`;

            tasks.push(newTask);
            renderAll();

            // Close after short delay so user sees the success message
            setTimeout(() => {
                closeModal('add-modal');
                setTrelloStatus('Card created ✓', 'ok');
            }, 1200);

        } catch (e) {
            btn.disabled = false;
            btn.textContent = '+ Add Task';
            statusEl.style.background = '#f5656510';
            statusEl.style.color = 'var(--red)';
            statusEl.style.border = '1px solid #f5656530';
            statusEl.textContent = '✗ Trello failed: ' + e.message + ' — task saved locally only';
            // Still save locally
            tasks.push(newTask);
            renderAll();
        }
        return;
    }

    // Not connected — save locally only
    tasks.push(newTask);
    closeModal('add-modal');
    renderAll();
}

function runDailyCheck() {
    const ct = { id: 'T-' + String(taskCounter++).padStart(3, '0'), title: 'Daily check — ' + new Date().toLocaleTimeString(), agent: 'opus-agent', status: 'inprogress', tags: ['daily', 'strategy'], inputTok: randInt(18000, 24000), outputTok: randInt(1800, 3200), time: new Date().getHours().toString().padStart(2, '0') + ':' + new Date().getMinutes().toString().padStart(2, '0'), date: today(0) };
    tasks.push(ct); renderAll();
    setTimeout(() => { ct.status = 'done'; renderAll() }, 1200);
}

// Close on overlay
['detail-modal', 'add-modal', 'reassign-modal', 'metrics-modal', 'agent-detail-modal'].forEach(id => {
    document.getElementById(id).addEventListener('click', e => { if (e.target === e.currentTarget) closeModal(id) });
});

// Init date pickers
const todayStr = today(0);
const weekAgoStr = today(-7);
document.getElementById('range-from').value = weekAgoStr;
document.getElementById('range-to').value = todayStr;

// Apply any pending scheduled changes
applyScheduledChanges();
renderAll();
