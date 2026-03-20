/* === MeshCore Analyzer — home.js (My Mesh Dashboard) === */
'use strict';

(function () {
  let searchTimeout = null;
  let miniMap = null;
  let searchAbort = null; // AbortController for document-level listeners

  const PREF_KEY = 'meshcore-user-level';
  const MY_NODES_KEY = 'meshcore-my-nodes'; // [{pubkey, name, addedAt}]

  function getMyNodes() {
    try { return JSON.parse(localStorage.getItem(MY_NODES_KEY)) || []; } catch { return []; }
  }
  function saveMyNodes(nodes) { localStorage.setItem(MY_NODES_KEY, JSON.stringify(nodes)); }
  function addMyNode(pubkey, name) {
    const nodes = getMyNodes();
    if (!nodes.find(n => n.pubkey === pubkey)) {
      nodes.push({ pubkey, name: name || pubkey.slice(0, 12), addedAt: new Date().toISOString() });
      saveMyNodes(nodes);
    }
  }
  function removeMyNode(pubkey) {
    saveMyNodes(getMyNodes().filter(n => n.pubkey !== pubkey));
  }
  function isMyNode(pubkey) { return getMyNodes().some(n => n.pubkey === pubkey); }

  function isExperienced() { return localStorage.getItem(PREF_KEY) === 'experienced'; }
  function setLevel(level) { localStorage.setItem(PREF_KEY, level); }

  function init(container) {
    if (!localStorage.getItem(PREF_KEY)) {
      showChooser(container);
      return;
    }
    renderHome(container);
  }

  function showChooser(container) {
    container.innerHTML = `
      <section class="home-chooser">
        <h1>Welcome to Bay Area MeshCore Analyzer</h1>
        <p>How familiar are you with MeshCore?</p>
        <div class="chooser-options">
          <button class="chooser-btn new" id="chooseNew">
            <span class="chooser-icon">🌱</span>
            <strong>I\u2019m new</strong>
            <span>Show me setup guides and tips</span>
          </button>
          <button class="chooser-btn exp" id="chooseExp">
            <span class="chooser-icon">⚡</span>
            <strong>I know what I\u2019m doing</strong>
            <span>Just the analyzer, skip the guides</span>
          </button>
        </div>
      </section>`;
    document.getElementById('chooseNew').addEventListener('click', () => { setLevel('new'); renderHome(container); });
    document.getElementById('chooseExp').addEventListener('click', () => { setLevel('experienced'); renderHome(container); });
  }

  function renderHome(container) {
    const exp = isExperienced();
    const myNodes = getMyNodes();
    const hasNodes = myNodes.length > 0;

    container.innerHTML = `
      <section class="home-hero">
        <h1>${hasNodes ? 'My Mesh' : 'MeshCore Analyzer'}</h1>
        <p>${hasNodes ? 'Your nodes at a glance. Add more by searching below.' : 'Find your nodes to start monitoring them.'}</p>
        <div class="home-search-wrap">
          <input type="text" id="homeSearch" placeholder="Search by node name or public key…" autocomplete="off" aria-label="Search nodes" role="combobox" aria-expanded="false" aria-owns="homeSuggest" aria-autocomplete="list" aria-activedescendant="">
          <div class="home-suggest" id="homeSuggest" role="listbox"></div>
        </div>
      </section>

      ${hasNodes ? '<div class="my-nodes-grid" id="myNodesGrid"><div class="my-nodes-loading">Loading your nodes…</div></div>' : '<div class="my-nodes-grid" id="myNodesGrid"></div>'}

      ${!hasNodes ? `
        <div class="onboarding-prompt">
          <div class="onboard-icon">📡</div>
          <h2>Claim your first node</h2>
          <p>Search for your node above, or paste your public key. Once claimed, you'll see live status, signal quality, and who's hearing you.</p>
        </div>
      ` : ''}

      <div class="home-detail-area">
        <div class="home-health" id="homeHealth"></div>
        <div class="home-journey" id="homeJourney"></div>
      </div>

      <div class="home-stats" id="homeStats"></div>

      ${exp ? '' : `
      <section class="home-checklist">
        <h2>🚀 Getting on the mesh — SF Bay Area</h2>
        ${checklist()}
      </section>`}

      <section class="home-footer">
        <div class="home-footer-links">
          <a href="#/packets" class="home-footer-link">📦 Packets</a>
          <a href="#/map" class="home-footer-link">🗺️ Network Map</a>
          <a href="#/live" class="home-footer-link">🔴 Live</a>
          <a href="#/nodes" class="home-footer-link">📡 All Nodes</a>
          <a href="#/channels" class="home-footer-link">💬 Channels</a>
        </div>
        <div class="home-level-toggle">
          <small>${exp ? 'Want setup guides? ' : 'Already know MeshCore? '}
          <a href="#" id="toggleLevel" style="color:var(--accent)">${exp ? 'Show new user tips' : 'Hide guides'}</a></small>
        </div>
      </section>
    `;

    document.getElementById('toggleLevel')?.addEventListener('click', (e) => {
      e.preventDefault();
      setLevel(exp ? 'new' : 'experienced');
      renderHome(container);
    });

    setupSearch(container);
    loadStats();
    if (hasNodes) loadMyNodes();

    // Checklist accordion
    container.querySelectorAll('.checklist-q').forEach(q => {
      const toggle = () => {
        const item = q.parentElement;
        item.classList.toggle('open');
        q.setAttribute('aria-expanded', item.classList.contains('open'));
      };
      q.addEventListener('click', toggle);
      q.addEventListener('keydown', (e) => {
        if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); toggle(); }
      });
    });
  }

  function setupSearch(container) {
    const input = document.getElementById('homeSearch');
    const suggest = document.getElementById('homeSuggest');
    if (!input || !suggest) return;

    input.addEventListener('input', () => {
      clearTimeout(searchTimeout);
      const q = input.value.trim();
      if (!q) { suggest.classList.remove('open'); input.setAttribute('aria-expanded', 'false'); input.setAttribute('aria-activedescendant', ''); return; }
      searchTimeout = setTimeout(async () => {
        try {
          const data = await api('/nodes/search?q=' + encodeURIComponent(q), { ttl: 10000 });
          const nodes = data.nodes || [];
          if (!nodes.length) {
            suggest.innerHTML = '<div class="suggest-empty">No nodes found</div>';
          } else {
            suggest.innerHTML = nodes.slice(0, 10).map((n, idx) => {
              const claimed = isMyNode(n.public_key);
              return `<div class="suggest-item" role="option" id="suggest-${idx}" data-key="${n.public_key}" data-name="${escapeAttr(n.name || '')}">
                <div class="suggest-main">
                  <span class="suggest-name">${escapeHtml(n.name || 'Unknown')}</span>
                  <small class="suggest-key">${truncate(n.public_key, 16)}</small>
                </div>
                <div class="suggest-actions">
                  <span class="suggest-role badge-${n.role || 'unknown'}">${n.role || '?'}</span>
                  <button class="suggest-claim ${claimed ? 'claimed' : ''}" data-key="${n.public_key}" data-name="${escapeAttr(n.name || '')}" title="${claimed ? 'Remove from My Mesh' : 'Add to My Mesh'}">
                    ${claimed ? '✓ Mine' : '+ Claim'}
                  </button>
                </div>
              </div>`;
            }).join('');
          }
          suggest.classList.add('open');
          input.setAttribute('aria-expanded', 'true');
          input.setAttribute('aria-activedescendant', '');

          // Claim buttons
          suggest.querySelectorAll('.suggest-claim').forEach(btn => {
            btn.addEventListener('click', (e) => {
              e.stopPropagation();
              const pk = btn.dataset.key;
              const nm = btn.dataset.name;
              if (isMyNode(pk)) {
                removeMyNode(pk);
                btn.classList.remove('claimed');
                btn.textContent = '+ Claim';
              } else {
                addMyNode(pk, nm);
                btn.classList.add('claimed');
                btn.textContent = '✓ Mine';
              }
              loadMyNodes();
            });
          });
        } catch { suggest.classList.remove('open'); input.setAttribute('aria-expanded', 'false'); }
      }, 200);
    });

    suggest.addEventListener('click', (e) => {
      const item = e.target.closest('.suggest-item');
      if (!item || !item.dataset.key || e.target.closest('.suggest-claim')) return;
      suggest.classList.remove('open');
      input.setAttribute('aria-expanded', 'false');
      input.value = '';
      loadHealth(item.dataset.key);
    });

    // Use AbortController so re-calling setupSearch won't stack listeners
    if (searchAbort) searchAbort.abort();
    searchAbort = new AbortController();
    document.addEventListener('click', handleOutsideClick, { signal: searchAbort.signal });
  }

  function handleOutsideClick(e) {
    const suggest = document.getElementById('homeSuggest');
    const input = document.getElementById('homeSearch');
    if (suggest && !e.target.closest('.home-search-wrap')) {
      suggest.classList.remove('open');
      if (input) { input.setAttribute('aria-expanded', 'false'); input.setAttribute('aria-activedescendant', ''); }
    }
  }

  function destroy() {
    clearTimeout(searchTimeout);
    if (searchAbort) { searchAbort.abort(); searchAbort = null; }
    if (miniMap) { miniMap.remove(); miniMap = null; }
  }

  // ==================== MY NODES DASHBOARD ====================
  async function loadMyNodes() {
    const grid = document.getElementById('myNodesGrid');
    if (!grid) return;
    const myNodes = getMyNodes();

    // Update hero text dynamically
    const h1 = document.querySelector('.home-hero h1');
    const heroP = document.querySelector('.home-hero p');
    if (myNodes.length) {
      if (h1) h1.textContent = 'My Mesh';
      if (heroP) heroP.textContent = 'Your nodes at a glance. Add more by searching below.';
      // Hide onboarding prompt
      const onboard = document.querySelector('.onboarding-prompt');
      if (onboard) onboard.style.display = 'none';
    }

    if (!myNodes.length) {
      grid.innerHTML = '';
      return;
    }

    const cards = await Promise.all(myNodes.map(async (mn) => {
      try {
        const h = await api('/nodes/' + encodeURIComponent(mn.pubkey) + '/health', { ttl: 240000 });
        const node = h.node || {};
        const stats = h.stats || {};
        const obs = h.observers || [];

        const age = stats.lastHeard ? Date.now() - new Date(stats.lastHeard).getTime() : null;
        const status = age === null ? 'silent' : age < 3600000 ? 'healthy' : age < 86400000 ? 'degraded' : 'silent';
        const statusDot = status === 'healthy' ? '🟢' : status === 'degraded' ? '🟡' : '🔴';
        const statusText = status === 'healthy' ? 'Active' : status === 'degraded' ? 'Degraded' : 'Silent';
        const name = node.name || mn.name || truncate(mn.pubkey, 12);

        // SNR quality label
        const snrVal = stats.avgSnr;
        const snrLabel = snrVal != null ? (snrVal > 10 ? 'Excellent' : snrVal > 0 ? 'Good' : snrVal > -5 ? 'Marginal' : 'Poor') : null;
        const snrColor = snrVal != null ? (snrVal > 10 ? '#22c55e' : snrVal > 0 ? '#3b82f6' : snrVal > -5 ? '#f59e0b' : '#ef4444') : '#6b7280';

        // Build sparkline from recent packets (packet timestamps → hourly buckets)
        const sparkHtml = buildSparkline(h.recentPackets || []);

        return `<div class="my-node-card ${status}" data-key="${mn.pubkey}" tabindex="0" role="button">
          <div class="mnc-header">
            <div class="mnc-status">${statusDot}</div>
            <div class="mnc-name">${escapeHtml(name)}</div>
            <div class="mnc-role">${node.role || '?'}</div>
            <button class="mnc-remove" data-key="${mn.pubkey}" title="Remove from My Mesh" aria-label="Remove ${escapeAttr(name)} from My Mesh">✕</button>
          </div>
          <div class="mnc-status-text">${statusText}${stats.lastHeard ? ' · ' + timeAgo(stats.lastHeard) : ''}</div>
          <div class="mnc-metrics">
            <div class="mnc-metric">
              <div class="mnc-val">${stats.packetsToday ?? 0}</div>
              <div class="mnc-lbl">Packets today</div>
            </div>
            <div class="mnc-metric">
              <div class="mnc-val">${obs.length}</div>
              <div class="mnc-lbl">Observers</div>
            </div>
            <div class="mnc-metric">
              <div class="mnc-val" style="color:${snrColor}">${snrVal != null ? snrVal.toFixed(1) + ' dB' : '—'}</div>
              <div class="mnc-lbl">SNR${snrLabel ? ' · ' + snrLabel : ''}</div>
            </div>
            <div class="mnc-metric">
              <div class="mnc-val">${stats.avgHops != null ? stats.avgHops.toFixed(1) : '—'}</div>
              <div class="mnc-lbl">Avg hops</div>
            </div>
          </div>
          ${obs.length ? `<div class="mnc-observers"><strong>Heard by:</strong> ${obs.map(o => escapeHtml(o.observer_name || o.observer_id)).join(', ')}</div>` : ''}
          ${sparkHtml ? `<div class="mnc-spark">${sparkHtml}</div>` : ''}
          <div class="mnc-actions">
            <button class="mnc-btn" data-action="health" data-key="${mn.pubkey}">Full health →</button>
            <button class="mnc-btn" data-action="packets" data-key="${mn.pubkey}">View packets →</button>
          </div>
        </div>`;
      } catch {
        return `<div class="my-node-card silent" data-key="${mn.pubkey}" tabindex="0" role="button">
          <div class="mnc-header">
            <div class="mnc-status">❓</div>
            <div class="mnc-name">${escapeHtml(mn.name || truncate(mn.pubkey, 12))}</div>
            <button class="mnc-remove" data-key="${mn.pubkey}" title="Remove" aria-label="Remove ${escapeAttr(mn.name || truncate(mn.pubkey, 12))} from My Mesh">✕</button>
          </div>
          <div class="mnc-status-text">Could not load data</div>
        </div>`;
      }
    }));

    grid.innerHTML = cards.join('');

    // Wire up remove buttons
    grid.querySelectorAll('.mnc-remove').forEach(btn => {
      btn.addEventListener('click', (e) => {
        e.stopPropagation();
        removeMyNode(btn.dataset.key);
        loadMyNodes();
        // Update title if no nodes left
        const h1 = document.querySelector('.home-hero h1');
        if (h1 && !getMyNodes().length) h1.textContent = 'MeshCore Analyzer';
      });
    });

    // Wire up action buttons
    grid.querySelectorAll('.mnc-btn').forEach(btn => {
      btn.addEventListener('click', (e) => {
        e.stopPropagation();
        if (btn.dataset.action === 'health') loadHealth(btn.dataset.key);
        if (btn.dataset.action === 'packets') window.location.hash = '#/packets/' + btn.dataset.key;
      });
    });

    // Card click → health
    grid.querySelectorAll('.my-node-card').forEach(card => {
      const handler = (e) => {
        if (e.target.closest('.mnc-remove') || e.target.closest('.mnc-btn')) return;
        loadHealth(card.dataset.key);
      };
      card.addEventListener('click', handler);
      card.addEventListener('keydown', (e) => {
        if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handler(e); }
      });
    });
  }

  function buildSparkline(packets) {
    if (!packets.length) return '';
    // Group into hourly buckets over last 24h
    const now = Date.now();
    const buckets = new Array(24).fill(0);
    packets.forEach(p => {
      const t = new Date(p.timestamp || p.created_at).getTime();
      const hoursAgo = Math.floor((now - t) / 3600000);
      if (hoursAgo >= 0 && hoursAgo < 24) buckets[23 - hoursAgo]++;
    });
    const max = Math.max(...buckets, 1);
    const bars = buckets.map(v => {
      const h = Math.max(2, Math.round((v / max) * 24));
      const opacity = v > 0 ? 0.4 + (v / max) * 0.6 : 0.1;
      return `<div class="home-spark-bar" style="height:${h}px;opacity:${opacity}"></div>`;
    }).join('');
    return `<div class="home-spark-label">24h activity</div><div class="home-spark-bars">${bars}</div>`;
  }

  // ==================== STATS ====================
  async function loadStats() {
    try {
      const s = await api('/stats', { ttl: 10000 });
      const el = document.getElementById('homeStats');
      if (!el) return;
      el.innerHTML = `
        <div class="home-stat"><div class="val">${s.totalPackets ?? '—'}</div><div class="lbl">Packets</div></div>
        <div class="home-stat"><div class="val">${s.totalNodes ?? '—'}</div><div class="lbl">Nodes</div></div>
        <div class="home-stat"><div class="val">${s.totalObservers ?? '—'}</div><div class="lbl">Observers</div></div>
        <div class="home-stat"><div class="val">${s.packetsLast24h ?? '—'}</div><div class="lbl">Last 24h</div></div>
      `;
    } catch {}
  }

  // ==================== HEALTH DETAIL ====================
  async function loadHealth(pubkey) {
    const card = document.getElementById('homeHealth');
    const journey = document.getElementById('homeJourney');
    if (!card) return;
    card.innerHTML = '<p style="color:var(--text-muted);padding:12px">Loading…</p>';
    card.classList.add('visible');
    if (journey) journey.classList.remove('visible');

    try {
      const h = await api('/nodes/' + encodeURIComponent(pubkey) + '/health', { ttl: 240000 });
      const node = h.node || {};
      const stats = h.stats || {};
      const packets = h.recentPackets || [];
      const hasLocation = node.lat != null && node.lon != null;
      const observers = h.observers || [];
      const claimed = isMyNode(pubkey);

      let status = 'silent', color = 'red', statusMsg = 'Not heard in 24+ hours';
      if (stats.lastHeard) {
        const ageMs = Date.now() - new Date(stats.lastHeard).getTime();
        const ago = timeAgo(stats.lastHeard);
        if (ageMs < 3600000) { status = 'healthy'; color = 'green'; statusMsg = `Last heard ${ago}`; }
        else if (ageMs < 86400000) { status = 'degraded'; color = 'yellow'; statusMsg = `Last heard ${ago}`; }
        else { statusMsg = `Last heard ${ago}`; }
      }

      const snrVal = stats.avgSnr;
      const snrLabel = snrVal != null ? (snrVal > 10 ? 'Excellent' : snrVal > 0 ? 'Good' : snrVal > -5 ? 'Marginal' : 'Poor') : '';

      card.innerHTML = `
        <div class="health-banner ${color}">
          <span>${status === 'healthy' ? '✅' : status === 'degraded' ? '⚠️' : '❌'}</span>
          <span><strong>${escapeHtml(node.name || truncate(pubkey, 16))}</strong> — ${statusMsg}</span>
          ${!claimed ? `<button class="health-claim" data-key="${pubkey}" data-name="${escapeAttr(node.name || '')}">+ Add to My Mesh</button>` : ''}
        </div>
        <div class="health-body">
          <div class="health-metrics">
            <div class="health-metric"><div class="val">${stats.packetsToday ?? '—'}</div><div class="lbl">Packets Today</div></div>
            <div class="health-metric"><div class="val">${observers.length}</div><div class="lbl">Observers</div></div>
            <div class="health-metric"><div class="val">${stats.lastHeard ? timeAgo(stats.lastHeard) : '—'}</div><div class="lbl">Last seen</div></div>
            <div class="health-metric"><div class="val">${snrVal != null ? snrVal.toFixed(1) + ' dB' : '—'}</div><div class="lbl">Avg SNR${snrLabel ? ' · ' + snrLabel : ''}</div></div>
            <div class="health-metric"><div class="val">${stats.avgHops != null ? stats.avgHops.toFixed(1) : '—'}</div><div class="lbl">Avg Hops</div></div>
          </div>
          ${observers.length ? `<div class="health-observers"><strong>Heard by:</strong> ${observers.map(o => escapeHtml(o.observer_name || o.observer_id)).join(', ')}</div>` : ''}
          ${hasLocation ? '<div class="health-map" id="healthMap"></div>' : ''}
          <div class="health-timeline">
            <h3>Recent Activity</h3>
            ${packets.length ? packets.slice(0, 10).map(p => {
              const decoded = p.decoded_json ? JSON.parse(p.decoded_json) : {};
              const obsId = p.observer_name || p.observer_id || '?';
              return `<div class="timeline-item" tabindex="0" role="button" data-pkt='${JSON.stringify({
                from: node.name || truncate(pubkey, 12),
                observers: [obsId],
                type: p.payload_type,
                time: p.timestamp || p.created_at
              }).replace(/'/g, '&#39;')}'>
                <span class="badge" style="background:var(--type-${payloadTypeColor(p.payload_type)})">${escapeHtml(payloadTypeName(p.payload_type))}</span>
                <span>via ${escapeHtml(obsId)}</span>
                <span class="time">${timeAgo(p.timestamp || p.created_at)}</span>
                <span class="snr">${p.snr != null ? p.snr.toFixed(1) + ' dB' : ''}</span>
              </div>`;
            }).join('') : '<p style="color:var(--text-muted);font-size:.85rem">No recent packets found for this node.</p>'}
          </div>
        </div>
      `;

      // Claim button in health detail
      card.querySelector('.health-claim')?.addEventListener('click', (e) => {
        e.stopPropagation();
        addMyNode(pubkey, node.name);
        e.target.remove();
        loadMyNodes();
        const h1 = document.querySelector('.home-hero h1');
        if (h1) h1.textContent = 'My Mesh';
      });

      // Mini map
      if (hasLocation && typeof L !== 'undefined') {
        if (miniMap) { miniMap.remove(); miniMap = null; }
        const mapEl = document.getElementById('healthMap');
        if (mapEl) {
          miniMap = L.map(mapEl, { zoomControl: false, attributionControl: false }).setView([node.lat, node.lon], 12);
          L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png').addTo(miniMap);
          L.marker([node.lat, node.lon]).addTo(miniMap);
          setTimeout(() => miniMap.invalidateSize(), 100);
        }
      }

      // Scroll to health card
      card.scrollIntoView({ behavior: 'smooth', block: 'start' });

      // Timeline click/keyboard → journey
      card.querySelectorAll('.timeline-item').forEach(item => {
        const activate = () => { try { showJourney(JSON.parse(item.dataset.pkt)); } catch {} };
        item.addEventListener('click', activate);
        item.addEventListener('keydown', (e) => {
          if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); activate(); }
        });
      });
    } catch (e) {
      card.innerHTML = '<p style="color:var(--status-red, #ef4444);padding:12px">Failed to load node health.</p>';
    }
  }

  function showJourney(data) {
    const el = document.getElementById('homeJourney');
    if (!el) return;
    const nodes = [];
    nodes.push({ name: data.from, meta: 'Sender' });
    if (data.observers && data.observers.length) {
      data.observers.forEach(o => nodes.push({ name: o, meta: 'Observer' }));
    }
    const flow = nodes.map((n, i) => {
      const nodeHtml = `<div class="journey-node"><div class="node-name">${escapeHtml(n.name)}</div><div class="node-meta">${n.meta}</div></div>`;
      return i < nodes.length - 1 ? nodeHtml + '<div class="journey-arrow"></div>' : nodeHtml;
    }).join('');
    el.innerHTML = `<div class="journey-title">Packet Journey — ${escapeHtml(payloadTypeName(data.type))}</div><div class="journey-flow">${flow}</div>`;
    el.classList.add('visible');
  }

  // ==================== HELPERS ====================
  function escapeHtml(s) { return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;'); }
  function escapeAttr(s) { return String(s).replace(/"/g,'&quot;').replace(/'/g,'&#39;'); }
  function timeSinceMs(d) { return Date.now() - d.getTime(); }

  function checklist() {
    const items = [
      { q: '💬 First: Join the Bay Area MeshCore Discord',
        a: '<p>The community Discord is the best place to get help and find local mesh enthusiasts.</p><p><a href="https://discord.gg/q59JzsYTst" target="_blank" rel="noopener" style="color:var(--accent);font-weight:600">Join the Discord ↗</a></p><p>Start with <strong>#intro-to-meshcore</strong> — it has detailed setup instructions.</p>' },
      { q: '🔵 Step 1: Connect via Bluetooth',
        a: '<p>Flash <strong>BLE companion</strong> firmware from <a href="https://flasher.meshcore.co.uk/" target="_blank" rel="noopener" style="color:var(--accent)">MeshCore Flasher</a>.</p><ul><li>Screenless devices: default PIN <code>123456</code></li><li>Screen devices: random PIN shown on display</li><li>If pairing fails: forget device, reboot, re-pair</li></ul>' },
      { q: '📻 Step 2: Set the right frequency preset',
        a: '<p><strong>US Recommended:</strong></p><div style="margin:8px 0;padding:8px 12px;background:var(--surface-1);border-radius:6px;font-family:var(--mono);font-size:.85rem">910.525 MHz · BW 62.5 kHz · SF 7 · CR 5</div><p>Select <strong>"US Recommended"</strong> in the app or flasher.</p>' },
      { q: '📡 Step 3: Advertise yourself',
        a: '<p>Tap the signal icon → <strong>Flood</strong> to broadcast your node to the mesh. Companions only advert when you trigger it manually.</p>' },
      { q: '🔁 Step 4: Check "Heard N repeats"',
        a: '<ul><li><strong>"Sent"</strong> = transmitted, no confirmation</li><li><strong>"Heard 0 repeats"</strong> = no repeater picked it up</li><li><strong>"Heard 1+ repeats"</strong> = you\'re on the mesh!</li></ul>' },
      { q: '📍 Repeaters near you?',
        a: '<p><a href="#/map" style="color:var(--accent)">Check the network map</a> to see active repeaters.</p>' }
    ];
    return items.map(i => `<div class="checklist-item"><div class="checklist-q" role="button" tabindex="0" aria-expanded="false">${i.q}</div><div class="checklist-a">${i.a}</div></div>`).join('');
  }

  registerPage('home', { init, destroy });
})();
