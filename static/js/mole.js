/* Mole UI helpers shared by every page. No framework, no build step. */
(function () {
  'use strict';
  var Mole = {};

  Mole.esc = function (s) {
    if (s === null || s === undefined) return '';
    return String(s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  };

  // api performs a JSON request and throws an Error with the server's message.
  Mole.api = async function (method, url, body) {
    var opts = { method: method, headers: { 'Accept': 'application/json' } };
    if (body !== undefined) {
      opts.headers['Content-Type'] = 'application/json';
      opts.body = JSON.stringify(body);
    }
    var res = await fetch(url, opts);
    if (res.status === 204) return null;
    var data = null;
    try { data = await res.json(); } catch (e) { /* non-JSON */ }
    if (!res.ok || (data && data.success === false)) {
      var msg = 'HTTP ' + res.status;
      if (data && data.error) msg = typeof data.error === 'string' ? data.error : (data.error.message || msg);
      throw new Error(msg);
    }
    return data;
  };

  var toastTimer = null;
  Mole.toast = function (msg, ok) {
    var el = document.getElementById('mole-toast');
    if (!el) { alert(msg); return; }
    el.textContent = msg;
    el.className = 'fixed bottom-4 right-4 z-[60] max-w-sm px-4 py-3 rounded-lg shadow-lg text-sm ' +
      (ok === false ? 'bg-red-600 text-white' : 'bg-gray-900 text-white');
    clearTimeout(toastTimer);
    toastTimer = setTimeout(function () { el.classList.add('hidden'); }, ok === false ? 7000 : 3500);
  };

  Mole.fmtBytes = function (b) {
    b = b || 0;
    if (b >= 1073741824) return (b / 1073741824).toFixed(1) + ' GB';
    if (b >= 1048576) return (b / 1048576).toFixed(1) + ' MB';
    if (b >= 1024) return (b / 1024).toFixed(1) + ' KB';
    return b + ' B';
  };

  Mole.fmtMs = function (ms) {
    ms = ms || 0;
    if (ms < 1000) return ms + ' ms';
    var s = Math.round(ms / 1000);
    if (s < 60) return s + 's';
    var m = Math.floor(s / 60);
    if (m < 60) return m + 'm ' + (s % 60) + 's';
    return Math.floor(m / 60) + 'h ' + (m % 60) + 'm';
  };

  Mole.statusBadge = function (status) {
    return '<span class="status-badge status-badge-' + Mole.esc(status) + '">' + Mole.esc(status) + '</span>';
  };

  Mole.lines = function (text) {
    return (text || '').split('\n').map(function (x) { return x.trim(); }).filter(Boolean);
  };

  Mole.copy = function (text) {
    if (navigator.clipboard) navigator.clipboard.writeText(text).then(function () { Mole.toast('Copied'); });
  };

  function opt(value, label, selected) {
    return '<option value="' + Mole.esc(value) + '"' + (selected ? ' selected' : '') + '>' + Mole.esc(label) + '</option>';
  }
  Mole.options = function (values, selected, labels) {
    return values.map(function (v) { return opt(v, (labels && labels[v]) || v, v === selected); }).join('');
  };

  var inputCls = 'px-2 py-1.5 border border-gray-300 rounded-md text-sm focus:ring-2 focus:ring-indigo-500 focus:outline-none disabled:bg-gray-100 disabled:text-gray-500';

  /* ── Extraction field editor ──────────────────────────────────────── */
  // fieldEditor renders an editable list of extraction fields into container
  // and returns {get: function() -> fields[]}. List fields get one nested level.
  Mole.fieldEditor = function (container, fields, opts) {
    opts = opts || {};
    var types = opts.types || ['text', 'html', 'attr', 'url', 'number', 'price', 'date', 'exists', 'list'];
    var editable = opts.editable !== false;
    var dis = editable ? '' : ' disabled';

    function rowHTML(f, nested) {
      f = f || {};
      var t = f.type || 'text';
      return '<div class="fe-row border border-gray-200 rounded-md p-2 bg-white space-y-2" data-nested="' + (nested ? 1 : 0) + '">' +
        '<div class="grid grid-cols-12 gap-2 items-center">' +
        '<input data-k="name" placeholder="field name" value="' + Mole.esc(f.name) + '" class="col-span-3 font-mono ' + inputCls + '"' + dis + '>' +
        '<input data-k="selector" placeholder="' + (nested ? 'relative selector (empty = item)' : 'CSS selector or xpath://…') + '" value="' + Mole.esc(f.selector) + '" class="col-span-5 font-mono ' + inputCls + '"' + dis + '>' +
        '<select data-k="type" class="col-span-2 bg-white ' + inputCls + '"' + dis + '>' + Mole.options(nested ? types.filter(function (x) { return x !== 'list'; }) : types, t) + '</select>' +
        '<label class="col-span-1 text-xs text-gray-600 flex items-center gap-1" title="Return every match as an array"><input type="checkbox" data-k="multiple"' + (f.multiple ? ' checked' : '') + dis + '> all</label>' +
        (editable ? '<button type="button" data-act="remove" class="col-span-1 text-gray-400 hover:text-red-600 text-lg leading-none" title="Remove">&times;</button>' : '<span></span>') +
        '</div>' +
        '<div class="grid grid-cols-12 gap-2 items-center">' +
        '<input data-k="attr" placeholder="attribute (href, src, content…)" value="' + Mole.esc(f.attr) + '" class="col-span-4 font-mono ' + inputCls + ' fe-attr"' + dis + '>' +
        '<input data-k="regex" placeholder="regex (optional; first group is kept)" value="' + Mole.esc(f.regex) + '" class="col-span-8 font-mono ' + inputCls + '"' + dis + '>' +
        '</div>' +
        (nested ? '' : '<div class="fe-sub hidden ml-4 pl-3 border-l-2 border-indigo-200 space-y-2"><div class="fe-sub-rows space-y-2"></div>' +
          (editable ? '<button type="button" data-act="addsub" class="text-xs text-indigo-600 hover:text-indigo-800 font-medium">+ Add item field</button>' : '') + '</div>') +
        '</div>';
    }

    function syncRow(row) {
      var type = row.querySelector('[data-k="type"]').value;
      var attr = row.querySelector('.fe-attr');
      attr.classList.toggle('opacity-40', !(type === 'attr' || type === 'url'));
      var sub = row.querySelector(':scope > .fe-sub');
      if (sub) sub.classList.toggle('hidden', type !== 'list');
    }

    function addRow(parent, f, nested) {
      var wrap = document.createElement('div');
      wrap.innerHTML = rowHTML(f, nested);
      var row = wrap.firstChild;
      parent.appendChild(row);
      if (!nested && f && f.fields) f.fields.forEach(function (sf) { addRow(row.querySelector('.fe-sub-rows'), sf, true); });
      syncRow(row);
      return row;
    }

    container.innerHTML = '<div class="fe-rows space-y-2"></div>' +
      (editable ? '<button type="button" data-act="add" class="mt-2 text-xs text-indigo-600 hover:text-indigo-800 font-medium">+ Add field</button>' : '');
    var rows = container.querySelector('.fe-rows');
    (fields || []).forEach(function (f) { addRow(rows, f, false); });

    container.addEventListener('click', function (e) {
      var act = e.target.getAttribute('data-act');
      if (act === 'add') addRow(rows, { type: 'text' }, false);
      if (act === 'addsub') addRow(e.target.closest('.fe-sub').querySelector('.fe-sub-rows'), { type: 'text' }, true);
      if (act === 'remove') e.target.closest('.fe-row').remove();
    });
    container.addEventListener('change', function (e) {
      if (e.target.getAttribute('data-k') === 'type') syncRow(e.target.closest('.fe-row'));
    });

    function read(row) {
      var get = function (k) { return row.querySelector(':scope > div [data-k="' + k + '"]'); };
      var f = { name: get('name').value.trim(), selector: get('selector').value.trim(), type: get('type').value };
      if (f.type === 'text') delete f.type;
      if (get('multiple').checked) f.multiple = true;
      var attr = get('attr').value.trim();
      if (attr && (f.type === 'attr' || f.type === 'url')) f.attr = attr;
      var re = get('regex').value.trim();
      if (re) f.regex = re;
      if (f.type === 'list') {
        f.fields = Array.prototype.map.call(row.querySelectorAll(':scope > .fe-sub > .fe-sub-rows > .fe-row'), read)
          .filter(function (x) { return x.name; });
      }
      return f;
    }
    return {
      get: function () {
        return Array.prototype.map.call(rows.querySelectorAll(':scope > .fe-row'), read).filter(function (f) { return f.name; });
      }
    };
  };

  /* ── Browser actions editor ───────────────────────────────────────── */
  Mole.actionsEditor = function (container, actions, opts) {
    opts = opts || {};
    var types = opts.types || ['wait', 'wait_for_selector', 'click', 'scroll', 'type', 'press', 'evaluate'];
    var editable = opts.editable !== false;
    var dis = editable ? '' : ' disabled';
    var hints = {
      wait: 'waits <ms> milliseconds', wait_for_selector: 'waits until the selector appears',
      click: 'clicks the element', scroll: 'value: bottom, top or pixels; count repeats (lazy loading)',
      type: 'fills the input with value', press: 'presses a key (value: Enter, Tab…)', evaluate: 'runs JavaScript; the result is saved'
    };
    function rowHTML(a) {
      a = a || { type: 'click' };
      return '<div class="ae-row grid grid-cols-12 gap-2 items-start bg-white border border-gray-200 rounded-md p-2">' +
        '<select data-k="type" class="col-span-3 bg-white ' + inputCls + '"' + dis + '>' + Mole.options(types, a.type) + '</select>' +
        '<input data-k="selector" placeholder="selector" value="' + Mole.esc(a.selector) + '" class="col-span-3 font-mono ' + inputCls + '"' + dis + '>' +
        '<input data-k="value" placeholder="value / script" value="' + Mole.esc(a.script || a.value) + '" class="col-span-3 font-mono ' + inputCls + '"' + dis + '>' +
        '<input data-k="ms" type="number" min="0" placeholder="ms" value="' + (a.ms || '') + '" class="col-span-1 ' + inputCls + '"' + dis + '>' +
        '<input data-k="count" type="number" min="0" placeholder="×" value="' + (a.count || '') + '" class="col-span-1 ' + inputCls + '"' + dis + '>' +
        (editable ? '<button type="button" data-act="remove" class="col-span-1 text-gray-400 hover:text-red-600 text-lg leading-none">&times;</button>' : '<span></span>') +
        '<p class="ae-hint col-span-12 text-xs text-gray-400 -mt-1"></p></div>';
    }
    function sync(row) { row.querySelector('.ae-hint').textContent = hints[row.querySelector('[data-k="type"]').value] || ''; }
    container.innerHTML = '<div class="ae-rows space-y-2"></div>' +
      (editable ? '<button type="button" data-act="add" class="mt-2 text-xs text-indigo-600 hover:text-indigo-800 font-medium">+ Add action</button>' : '');
    var rows = container.querySelector('.ae-rows');
    function add(a) { var w = document.createElement('div'); w.innerHTML = rowHTML(a); rows.appendChild(w.firstChild); sync(rows.lastChild); }
    (actions || []).forEach(add);
    container.addEventListener('click', function (e) {
      var act = e.target.getAttribute('data-act');
      if (act === 'add') add({ type: 'click' });
      if (act === 'remove') e.target.closest('.ae-row').remove();
    });
    container.addEventListener('change', function (e) { if (e.target.getAttribute('data-k') === 'type') sync(e.target.closest('.ae-row')); });
    return {
      get: function () {
        return Array.prototype.map.call(rows.querySelectorAll('.ae-row'), function (row) {
          var g = function (k) { return row.querySelector('[data-k="' + k + '"]').value.trim(); };
          var a = { type: g('type') };
          if (g('selector')) a.selector = g('selector');
          if (g('value')) { if (a.type === 'evaluate') a.script = g('value'); else a.value = g('value'); }
          if (g('ms')) a.ms = parseInt(g('ms'), 10);
          if (g('count')) a.count = parseInt(g('count'), 10);
          return a;
        });
      }
    };
  };

  /* ── Checkbox group ───────────────────────────────────────────────── */
  Mole.checkboxGroup = function (container, values, selected, opts) {
    opts = opts || {};
    var sel = {};
    (selected || []).forEach(function (v) { sel[v] = true; });
    container.innerHTML = values.map(function (v) {
      return '<label class="inline-flex items-center gap-1.5 mr-4 mb-1 text-sm text-gray-700"><input type="checkbox" value="' + Mole.esc(v) + '"' +
        (sel[v] ? ' checked' : '') + (opts.editable === false ? ' disabled' : '') + ' class="h-4 w-4 text-indigo-600 border-gray-300 rounded"> ' +
        Mole.esc((opts.labels && opts.labels[v]) || v) + '</label>';
    }).join('');
    return {
      get: function () {
        return Array.prototype.filter.call(container.querySelectorAll('input:checked'), function () { return true; })
          .map(function (i) { return i.value; });
      }
    };
  };

  /* ── Schedule editor (create and edit) ────────────────────────────── */
  // scheduleEditor renders the schedule form and returns {get: () -> ScheduleInput}.
  Mole.scheduleEditor = function (container, sch, catalog) {
    sch = sch || {};
    var t = sch.template || {};
    var st = t.settings || {};
    var lim = t.limits || {};
    var wh = t.webhook || {};
    var fetchMode = st.use_headless_browser ? 'browser' : (st.headless_fallback ? 'fallback' : 'http');
    var presets = [['0 * * * *', 'Every hour'], ['0 */6 * * *', 'Every 6 hours'], ['0 3 * * *', 'Daily at 03:00'],
      ['0 9 * * 6', 'Weekly (Saturday 09:00)'], ['0 4 1 * *', 'Monthly (1st, 04:00)']];
    var cls = 'w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:ring-2 focus:ring-indigo-500 focus:outline-none';
    container.innerHTML =
      '<div class="grid grid-cols-1 md:grid-cols-2 gap-4">' +
      '<label class="block text-sm font-medium text-gray-700">Name<input data-s="name" class="mt-1 ' + cls + '" value="' + Mole.esc(sch.name) + '" placeholder="Phone prices on shop.ir"></label>' +
      '<label class="block text-sm font-medium text-gray-700">Start URL<input data-s="url" required class="mt-1 ' + cls + '" value="' + Mole.esc(t.target_url) + '" placeholder="https://shop.ir/category/phones"></label>' +
      '<div><label class="block text-sm font-medium text-gray-700">When</label><div class="mt-1 flex gap-2">' +
      '<select data-s="preset" class="' + cls + ' bg-white w-1/2"><option value="">Custom…</option>' + presets.map(function (p) { return '<option value="' + p[0] + '"' + (p[0] === (sch.cron || '0 3 * * *') ? ' selected' : '') + '>' + p[1] + '</option>'; }).join('') + '</select>' +
      '<input data-s="cron" required class="' + cls + ' font-mono w-1/2" value="' + Mole.esc(sch.cron || '0 3 * * *') + '" placeholder="0 3 * * *"></div>' +
      '<p class="text-xs text-gray-400 mt-1">Cron: minute hour day month weekday, or @hourly / @daily / "@every 6h".</p></div>' +
      '<label class="block text-sm font-medium text-gray-700">Time zone<input data-s="tz" class="mt-1 ' + cls + '" value="' + Mole.esc(sch.timezone || 'Asia/Tehran') + '"></label>' +
      '<div class="grid grid-cols-2 gap-3"><label class="block text-sm font-medium text-gray-700">Max depth<input data-s="depth" type="number" min="0" class="mt-1 ' + cls + '" value="' + (t.max_depth || 5) + '"></label>' +
      '<label class="block text-sm font-medium text-gray-700">Max pages / run<input data-s="maxpages" type="number" min="0" class="mt-1 ' + cls + '" value="' + (lim.max_pages || '') + '" placeholder="unlimited"></label></div>' +
      '<label class="block text-sm font-medium text-gray-700">Fetching<select data-s="fetch" class="mt-1 bg-white ' + cls + '">' +
      Mole.options(['http', 'fallback', 'browser'], fetchMode, { http: 'Fast HTTP', fallback: 'HTTP, browser if blocked', browser: 'Headless browser' }) + '</select></label>' +
      '<label class="block text-sm font-medium text-gray-700">Proxy pool<select data-s="pool" class="mt-1 bg-white ' + cls + '"><option value="">Default</option><option value="auto">Automatic routing</option><option value="direct">Direct</option>' +
      (catalog.proxy_pools || []).map(function (n) { return '<option value="' + Mole.esc(n) + '"' + (st.proxy_pool === n ? ' selected' : '') + '>' + Mole.esc(n) + '</option>'; }).join('') + '</select></label>' +
      '<label class="block text-sm font-medium text-gray-700">Region<input data-s="region" class="mt-1 ' + cls + '" value="' + Mole.esc(t.region) + '" placeholder="any worker (or auto, ir, intl…)"></label>' +
      '</div>' +
      '<div class="mt-4 flex flex-wrap gap-x-6 gap-y-2 text-sm text-gray-700">' +
      '<label class="flex items-center gap-2"><input data-s="enabled" type="checkbox"' + (sch.enabled === false ? '' : ' checked') + ' class="h-4 w-4 text-indigo-600 rounded"> Enabled</label>' +
      '<label class="flex items-center gap-2"><input data-s="changes" type="checkbox"' + (sch.change_detection === false ? '' : ' checked') + ' class="h-4 w-4 text-indigo-600 rounded"> Detect changes between runs</label>' +
      '<label class="flex items-center gap-2"><input data-s="skip" type="checkbox"' + (sch.skip_if_running === false ? '' : ' checked') + ' class="h-4 w-4 text-indigo-600 rounded"> Skip a run while the previous one is active</label>' +
      '<label class="flex items-center gap-2"><input data-s="main" type="checkbox"' + (st.only_main_content ? ' checked' : '') + ' class="h-4 w-4 text-indigo-600 rounded"> Main content only</label>' +
      '</div>' +
      '<div class="mt-4"><label class="block text-sm font-medium text-gray-700 mb-1">Outputs</label><div data-s="formats"></div></div>' +
      '<details class="mt-4"' + ((st.extract_fields || []).length ? ' open' : '') + '><summary class="text-sm font-medium text-gray-700 cursor-pointer">Structured extraction</summary>' +
      '<label class="block text-xs text-gray-500 mt-2">Only on URLs matching (regex)<input data-s="match" class="mt-1 font-mono ' + cls + '" value="' + Mole.esc(st.extract_url_match) + '" placeholder="/product/"></label>' +
      '<div data-s="fields" class="mt-2"></div></details>' +
      '<details class="mt-4"' + (wh.url ? ' open' : '') + '><summary class="text-sm font-medium text-gray-700 cursor-pointer">Webhook</summary>' +
      '<div class="grid grid-cols-1 md:grid-cols-3 gap-3 mt-2"><input data-s="whurl" type="url" class="md:col-span-2 ' + cls + '" value="' + Mole.esc(wh.url) + '" placeholder="https://app.example.com/hooks/mole">' +
      '<input data-s="whsecret" class="font-mono ' + cls + '" value="' + Mole.esc(wh.secret) + '" placeholder="signing secret"></div><div data-s="events" class="mt-2"></div></details>';

    var q = function (k) { return container.querySelector('[data-s="' + k + '"]'); };
    q('preset').addEventListener('change', function () { if (this.value) q('cron').value = this.value; });
    q('cron').addEventListener('input', function () { q('preset').value = ''; });
    var formats = Mole.checkboxGroup(q('formats'), catalog.formats, st.formats || ['markdown'],
      { labels: { markdown: 'Markdown', html: 'Clean HTML', text: 'Text', links: 'Links', metadata: 'Metadata' } });
    var fields = Mole.fieldEditor(q('fields'), st.extract_fields || [], { types: catalog.field_types });
    var events = Mole.checkboxGroup(q('events'), catalog.webhook_events, wh.events || []);

    return {
      get: function () {
        var settings = Object.assign({}, st);
        delete settings.use_headless_browser; delete settings.headless_fallback;
        var fm = q('fetch').value;
        if (fm === 'browser') settings.use_headless_browser = true;
        if (fm === 'fallback') settings.headless_fallback = true;
        settings.proxy_pool = q('pool').value || undefined;
        settings.only_main_content = q('main').checked || undefined;
        settings.formats = formats.get();
        settings.extract_fields = fields.get();
        settings.extract_url_match = q('match').value.trim() || undefined;
        if (!settings.extract_fields.length) delete settings.extract_fields;
        if (!settings.formats.length) delete settings.formats;
        var limits = Object.assign({}, lim);
        var mp = parseInt(q('maxpages').value, 10);
        if (mp > 0) limits.max_pages = mp; else delete limits.max_pages;
        var template = Object.assign({}, t, {
          target_url: q('url').value.trim(), max_depth: parseInt(q('depth').value, 10) || 0,
          region: q('region').value.trim() || undefined, settings: settings, limits: limits
        });
        var whURL = q('whurl').value.trim();
        template.webhook = whURL ? { url: whURL, secret: q('whsecret').value, events: events.get() } : undefined;
        return {
          name: q('name').value.trim(), cron: q('cron').value.trim(), timezone: q('tz').value.trim(),
          enabled: q('enabled').checked, change_detection: q('changes').checked, skip_if_running: q('skip').checked,
          template: template
        };
      }
    };
  };

  /* ── Engine status pill in the nav ────────────────────────────────── */
  async function refreshNav() {
    var text = document.getElementById('nav-engine-text');
    if (!text) return;
    try {
      var d = await Mole.api('GET', '/api/stats');
      var e = d.data.engine;
      var dot = document.getElementById('nav-engine-dot');
      dot.className = 'inline-block w-2 h-2 rounded-full align-middle mr-1 ' + (e.running > 0 ? 'bg-green-400 animate-pulse' : 'bg-gray-300');
      text.textContent = e.running > 0 || e.queued > 0 ? e.running + ' running · ' + e.queued + ' queued' : 'Idle';
      document.dispatchEvent(new CustomEvent('mole:stats', { detail: d.data }));
    } catch (err) {
      text.textContent = 'Offline';
    }
  }
  Mole.refreshNav = refreshNav;
  document.addEventListener('DOMContentLoaded', function () {
    refreshNav();
    setInterval(function () { if (!document.hidden) refreshNav(); }, 5000);
  });

  window.Mole = Mole;
})();
