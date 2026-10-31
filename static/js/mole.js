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

  /* ── Output format picker ─────────────────────────────────────────── */
  var contentFormats = {
    markdown: ['Markdown', 'Clean text for LLMs and RAG'],
    html: ['Clean HTML', 'Markup without scripts and styles'],
    text: ['Plain text', 'Text only'],
    links: ['Links', 'Every link on the page'],
    metadata: ['Metadata', 'Title, description, OpenGraph, JSON-LD, dates']
  };
  var detectorIcons = { product: '🛒', article: '📰', business: '🏢', job_posting: '💼', event: '🎫', faq: '❓' };

  // formatPicker renders the per-page outputs: content formats as toggle
  // pills and the detectors (catalog.detectors) as cards that say what each
  // returns. opts: {editable: bool, columns: 1|2|3}. Returns {get: () -> formats}.
  Mole.formatPicker = function (container, catalog, selected, opts) {
    opts = opts || {};
    var editable = opts.editable !== false;
    var detectors = catalog.detectors || [];
    var isDetector = {};
    detectors.forEach(function (d) { isDetector[d.format] = true; });
    var sel = {};
    (selected || []).forEach(function (f) { sel[f] = true; });
    var dis = editable ? '' : ' disabled';
    var grid = { 1: 'grid-cols-1', 2: 'grid-cols-1 sm:grid-cols-2', 3: 'grid-cols-1 sm:grid-cols-2 xl:grid-cols-3' }[opts.columns || 3];

    var pills = (catalog.formats || []).filter(function (f) { return !isDetector[f]; }).map(function (f) {
      var info = contentFormats[f] || [f, ''];
      return '<label class="fp-pill inline-flex items-center gap-2 px-3 py-1.5 mr-2 mb-2 rounded-full border text-sm select-none ' +
        (editable ? 'cursor-pointer' : 'opacity-70') + '" title="' + Mole.esc(info[1]) + '">' +
        '<input type="checkbox" class="h-3.5 w-3.5 text-indigo-600 rounded" value="' + Mole.esc(f) + '"' + (sel[f] ? ' checked' : '') + dis + '>' +
        Mole.esc(info[0]) + '</label>';
    }).join('');

    var cards = detectors.map(function (d) {
      var shown = d.fields.slice(0, 6), more = d.fields.length - shown.length;
      return '<label class="fp-card relative block rounded-lg border p-3 transition ' + (editable ? 'cursor-pointer hover:border-indigo-300' : 'opacity-70') + '">' +
        '<input type="checkbox" class="absolute top-3 right-3 h-4 w-4 text-indigo-600 rounded" value="' + Mole.esc(d.format) + '"' + (sel[d.format] ? ' checked' : '') + dis + '>' +
        '<div class="flex items-center gap-2 pr-6"><span class="text-lg leading-none">' + (detectorIcons[d.format] || '🔎') + '</span>' +
        '<span class="text-sm font-semibold text-gray-800">' + Mole.esc(d.label) + '</span></div>' +
        '<p class="mt-1 text-xs text-gray-500">' + Mole.esc(d.description) + '</p>' +
        '<div class="mt-2 flex flex-wrap gap-1" title="' + Mole.esc(d.fields.join(', ')) + '">' +
        shown.map(function (f) { return '<span class="px-1.5 py-0.5 rounded bg-gray-100 text-gray-600 font-mono text-[11px]">' + Mole.esc(f) + '</span>'; }).join('') +
        (more > 0 ? '<span class="px-1.5 py-0.5 text-[11px] text-gray-400">+' + more + ' more</span>' : '') +
        '</div></label>';
    }).join('');

    container.innerHTML =
      '<div class="text-xs font-medium uppercase tracking-wide text-gray-500 mb-2">Page content</div>' +
      '<div>' + pills + '</div>' +
      (detectors.length ?
        '<div class="flex flex-wrap items-baseline justify-between gap-2 mt-3 mb-2">' +
        '<div class="text-xs font-medium uppercase tracking-wide text-gray-500">Detect structured data <span class="normal-case font-normal text-gray-400">· no selectors needed</span></div>' +
        (editable ? '<div class="text-xs"><button type="button" data-fp="all" class="text-indigo-600 hover:underline">Select all</button>' +
          '<span class="text-gray-300 mx-1">·</span><button type="button" data-fp="none" class="text-indigo-600 hover:underline">Clear</button></div>' : '') +
        '</div>' +
        '<div class="grid gap-3 ' + grid + '">' + cards + '</div>' +
        '<p class="mt-2 text-xs text-gray-400">Each page runs the detector of its type (product pages the product detector, articles the article one; business and FAQ run on every page). Data comes from the JSON-LD, microdata and OpenGraph sites publish for search engines; product pages without it are read from their HTML. Results appear on each page, in exports (<code>product.price</code> … columns) and in change detection.</p>'
        : '');

    function paint() {
      container.querySelectorAll('.fp-pill').forEach(function (el) {
        var on = el.querySelector('input').checked;
        el.classList.toggle('bg-indigo-50', on); el.classList.toggle('border-indigo-400', on); el.classList.toggle('text-indigo-800', on);
        el.classList.toggle('bg-white', !on); el.classList.toggle('border-gray-300', !on); el.classList.toggle('text-gray-700', !on);
      });
      container.querySelectorAll('.fp-card').forEach(function (el) {
        var on = el.querySelector('input').checked;
        el.classList.toggle('bg-indigo-50', on); el.classList.toggle('border-indigo-400', on); el.classList.toggle('ring-1', on); el.classList.toggle('ring-indigo-400', on);
        el.classList.toggle('bg-white', !on); el.classList.toggle('border-gray-200', !on);
      });
    }
    container.addEventListener('change', paint);
    container.querySelectorAll('[data-fp]').forEach(function (b) {
      b.addEventListener('click', function () {
        var on = b.getAttribute('data-fp') === 'all';
        container.querySelectorAll('.fp-card input').forEach(function (i) { i.checked = on; });
        paint();
      });
    });
    paint();

    return {
      get: function () {
        var checked = {};
        container.querySelectorAll('input:checked').forEach(function (i) { checked[i.value] = true; });
        return (catalog.formats || []).filter(function (f) { return checked[f]; });
      }
    };
  };

  /* ── Tags ─────────────────────────────────────────────────────────── */
  var typeIcons = { product: '🛒', listing: '📋', article: '📰', job_posting: '💼', event: '🎫', faq: '❓', business: '🏢', home: '🏠', document: '📄', other: '·' };
  Mole.typeIcon = function (t) { return typeIcons[t] || '🏷'; };

  // isPageType reports whether a tag is one of the detected page types.
  Mole.isPageType = function (tag, catalog) {
    return (catalog.page_types || []).some(function (p) { return p.type === tag; });
  };

  // tagPill renders a tag: page-type tags with their icon, custom tags plain.
  Mole.tagPill = function (tag, catalog, extra) {
    var typed = Mole.isPageType(tag, catalog || {});
    return '<span class="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-xs ' +
      (typed ? 'bg-indigo-50 text-indigo-700' : 'bg-amber-50 text-amber-800') + (extra ? ' ' + extra : '') + '" dir="auto">' +
      (typed ? '<span>' + Mole.typeIcon(tag) + '</span>' : '#') + Mole.esc(tag) + '</span>';
  };

  // normalizeTag mirrors models.NormalizeTag: lower-case, words joined by "-".
  Mole.normalizeTag = function (t) { return (t || '').trim().toLowerCase().split(/\s+/).filter(Boolean).join('-'); };

  // tagRulesEditor edits [{pattern, tag}] rows. The tag input suggests the
  // page types; any other text is a custom tag. opts: {editable, onChange}.
  Mole.tagRulesEditor = function (container, rules, catalog, opts) {
    opts = opts || {};
    var editable = opts.editable !== false, dis = editable ? '' : ' disabled';
    var listId = 'tag-suggest-' + Math.random().toString(36).slice(2);
    var inputCls = 'px-3 py-2 border border-gray-300 rounded-md text-sm focus:ring-2 focus:ring-indigo-500 focus:outline-none disabled:bg-gray-100 disabled:text-gray-500';
    container.innerHTML =
      '<datalist id="' + listId + '">' + (catalog.page_types || []).map(function (p) {
        return '<option value="' + Mole.esc(p.type) + '">' + Mole.esc(p.label) + '</option>';
      }).join('') + '</datalist>' +
      '<div class="tr-rows space-y-2"></div>' +
      (editable ? '<button type="button" class="tr-add mt-2 text-sm text-indigo-600 hover:underline">+ Add rule</button>' : '');
    var rows = container.querySelector('.tr-rows');
    function add(r) {
      var row = document.createElement('div');
      row.className = 'tr-row flex items-center gap-2';
      row.innerHTML =
        '<input type="text" data-k="pattern" placeholder="URL regex, e.g. /gsp-\\d+/" class="flex-1 font-mono ' + inputCls + '"' + dis + '>' +
        '<span class="text-gray-400 text-sm">→</span>' +
        '<input type="text" data-k="tag" list="' + listId + '" placeholder="tag (product, sale…)" class="w-44 ' + inputCls + '"' + dis + '>' +
        (editable ? '<button type="button" class="tr-del px-2 py-2 text-gray-400 hover:text-red-600 text-lg leading-none" title="Remove">&times;</button>' : '');
      row.querySelector('[data-k="pattern"]').value = r.pattern || '';
      row.querySelector('[data-k="tag"]').value = r.tag || '';
      rows.appendChild(row);
    }
    (rules || []).forEach(add);
    if (!rows.children.length && editable) add({});
    function changed() { if (opts.onChange) opts.onChange(api.get()); }
    // on* handlers, not addEventListener: the form re-creates editors on reset.
    container.onclick = function (e) {
      if (e.target.classList.contains('tr-add')) { add({}); changed(); }
      if (e.target.classList.contains('tr-del')) { e.target.closest('.tr-row').remove(); if (!rows.children.length) add({}); changed(); }
    };
    container.oninput = changed;
    var api = {
      get: function () {
        return Array.prototype.map.call(rows.querySelectorAll('.tr-row'), function (row) {
          return { pattern: row.querySelector('[data-k="pattern"]').value.trim(), tag: Mole.normalizeTag(row.querySelector('[data-k="tag"]').value) };
        }).filter(function (r) { return r.pattern || r.tag; });
      }
    };
    return api;
  };

  // keepTagsPicker chooses keep_tags: none (keep every page), "auto" (the
  // types of the enabled detectors) or a list of tags. customTags() returns
  // the tags of the current rules, offered next to the page types.
  Mole.keepTagsPicker = function (container, selected, catalog, opts) {
    opts = opts || {};
    var editable = opts.editable !== false;
    var sel = {};
    (selected || []).forEach(function (t) { sel[t] = true; });
    function render() {
      var tags = (catalog.page_types || []).map(function (p) { return p.type; });
      (opts.customTags ? opts.customTags() : []).concat(Object.keys(sel)).forEach(function (t) {
        if (t && t !== 'auto' && tags.indexOf(t) < 0) tags.push(t);
      });
      var chip = function (value, label, title) {
        return '<label class="kt-chip inline-flex items-center gap-1.5 px-2.5 py-1 mr-2 mb-2 rounded-full border text-sm select-none ' +
          (editable ? 'cursor-pointer' : 'opacity-70') + '"' + (title ? ' title="' + Mole.esc(title) + '"' : '') + '>' +
          '<input type="checkbox" class="h-3.5 w-3.5 text-indigo-600 rounded" value="' + Mole.esc(value) + '"' + (sel[value] ? ' checked' : '') + (editable ? '' : ' disabled') + '>' +
          label + '</label>';
      };
      container.innerHTML =
        chip('auto', '✨ Types of the enabled detectors', 'Product pages when the Product detector is on, articles for Article, and so on') +
        tags.map(function (t) {
          return chip(t, (Mole.isPageType(t, catalog) ? Mole.typeIcon(t) + ' ' : '#') + Mole.esc(t));
        }).join('');
      paint();
    }
    function paint() {
      container.querySelectorAll('.kt-chip').forEach(function (el) {
        var on = el.querySelector('input').checked;
        el.classList.toggle('bg-indigo-50', on); el.classList.toggle('border-indigo-400', on); el.classList.toggle('text-indigo-800', on);
        el.classList.toggle('bg-white', !on); el.classList.toggle('border-gray-300', !on); el.classList.toggle('text-gray-700', !on);
      });
    }
    container.onchange = function (e) {
      if (e.target.type === 'checkbox') { sel[e.target.value] = e.target.checked; if (!e.target.checked) delete sel[e.target.value]; paint(); if (opts.onChange) opts.onChange(); }
    };
    render();
    return {
      refresh: render,
      get: function () { return Object.keys(sel).filter(function (t) { return sel[t]; }); }
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
