/* Crawl settings form: fills and reads the sections of templates/settings_form.html.
 * Shared by the job page (one crawl) and the schedule page (the crawl each run
 * creates). Inline handlers in the form call these functions, so they are global.
 *
 * The page defines, before DOMContentLoaded:
 *   defaultSettings   engine defaults (GET /v1/info default_settings)
 *   currentSettings   the settings being edited (null = all defaults)
 *   jobOptions        {priority, region, limits, webhook}
 *   pluginCatalog     plugins.List()
 *   catalog           GET /v1/info
 *   settingsEditable  false renders the form read-only
 *   jobSeedCount      seed URLs, for the POST payload preview
 * and then calls populateSettings(). buildPayload() returns the settings and
 * collectOptions() the other fields; validateSettingsForm() and
 * highlightServerError() point at invalid inputs, showAlert() reports results
 * in #settings-alert and #save-status.
 */
var requestMethod = 'GET';
var bodyTplVarRe = /\{\{(\w+)\}\}/g;
var bodyPrevVarNames = [];

/* ── Settings ── */
function populateSettings(){
    document.querySelectorAll('.def-val').forEach(function(el){
        var k=el.getAttribute('data-key');
        var v=defaultSettings[k];
        if(typeof v === 'boolean') el.textContent=v?'On':'Off';
        else if(k==='after_crawl_plugin'||k==='after_job_plugin') el.textContent=v?v:'None';
        else if((k==='max_pages'||k==='host_rate_limit')&&!v) el.textContent='unlimited';
        else if(k==='host_rate_limit') el.textContent=v+' req/s';
        else el.textContent=v!=null&&v!==''?v:'-';
    });
    var s=currentSettings||{};
    setVal('set-max-concurrent',s.max_concurrent_requests);
    setVal('set-request-timeout',s.request_timeout_seconds);
    setVal('set-politeness-delay',s.politeness_delay_ms);
    setVal('set-max-depth',s.max_depth);
    setVal('set-max-pages',s.max_pages);
    setVal('set-max-pages-domain',s.max_pages_per_domain);
    setVal('set-max-retries',s.max_retries);
    setVal('set-user-agent',s.user_agent);
    document.getElementById('set-url-include').value=(s.url_include_patterns||[]).join('\n');
    document.getElementById('set-url-exclude').value=(s.url_exclude_patterns||[]).join('\n');
    document.getElementById('set-extra-tracking').value=(s.extra_tracking_params||[]).join('\n');
    document.getElementById('set-skip-extensions').value=(s.skip_extensions||[]).join('\n');
    document.getElementById('skip-ext-count').textContent=(defaultSettings.skip_extensions||[]).length;
    setTri('set-skip-content-dup', s.skip_content_duplicates);
    setTri('set-respect-robots', s.respect_robots_txt);
    setFetchMode(s);
    setVal('set-headless-selector',s.headless_wait_selector);
    setTri('set-enable-semantic', s.enable_semantic_search);
    setTri('set-save-text-content', s.save_text_content);
    fillPluginSelect('set-after-crawl-plugin','after_crawl', s.after_crawl_plugin, 'after-crawl-plugin-desc', defaultSettings.after_crawl_plugin);
    fillPluginSelect('set-after-job-plugin','after_job', s.after_job_plugin, 'after-job-plugin-desc', defaultSettings.after_job_plugin);
    setTri('set-enable-word-extraction', s.enable_word_extraction);
    setTri('set-use-crawl-phrases-only', s.use_crawl_phrases_only);
    setTri('set-stemming', s.enable_stemming);
    setTri('set-lemmatization', s.enable_lemmatization);
    var lang=document.getElementById('set-language');
    lang.options[0].textContent='Default ('+({fa:'Persian',en:'English'}[defaultSettings.default_language]||defaultSettings.default_language||'—')+')';
    lang.value=s.default_language||'';
    setVal('set-request-body', s.request_body);
    renderHeaderRows(s.request_headers||[]);
    setRequestMethod(s.request_method||'GET');
    onBodyInput(s.request_body_vars||{});
    populateExtra(s);
}
function setVal(id,v){var e=document.getElementById(id);if(e)e.value=(v!=null&&v!=undefined)?v:'';}
function intVal(id){var v=document.getElementById(id).value.trim();return v===''?undefined:parseInt(v,10);}
function strVal(id){var v=document.getElementById(id).value.trim();return v===''?undefined:v;}
function linesVal(id){var v=document.getElementById(id).value.trim();if(!v)return undefined;var l=v.split('\n').map(function(x){return x.trim()}).filter(function(x){return x.length>0});return l.length?l:undefined;}

function pluginById(id){
    if(!id) return null;
    var list=pluginCatalog||[];
    for(var i=0;i<list.length;i++){if(list[i].id===id) return list[i];}
    return null;
}
// fillPluginSelect lists plugins with a hook. selected undefined/null means
// "use the engine default" (the option with value "__default__").
function fillPluginSelect(selectId, hook, selected, descId, def){
    var el=document.getElementById(selectId);
    if(!el) return;
    el.innerHTML='';
    var add=function(value,label){var o=document.createElement('option');o.value=value;o.textContent=label;el.appendChild(o);};
    var defPlugin=pluginById(def);
    add('__default__','Default ('+(def?(defPlugin&&defPlugin.name?defPlugin.name:def):'none')+')');
    add('','None');
    (pluginCatalog||[]).forEach(function(p){
        if(hook==='after_crawl' && !p.has_after_crawl) return;
        if(hook==='after_job' && !p.has_after_job) return;
        add(p.id, p.name?p.name+' ('+p.id+')':p.id);
    });
    if(selected && !pluginById(selected)) add(selected, selected+' (missing)');
    el.value=(selected===null||selected===undefined)?'__default__':selected;
    function syncDesc(){
        var desc=document.getElementById(descId);
        if(!desc) return;
        var id=el.value==='__default__'?def:el.value;
        var p=pluginById(id);
        desc.textContent=p&&p.description?p.description:'';
    }
    el.onchange=syncDesc;
    syncDesc();
}

function setRequestMethod(method){
    requestMethod=(method||'GET').toUpperCase()==='POST'?'POST':'GET';
    var hidden=document.getElementById('set-request-method');
    if(hidden) hidden.value=requestMethod;
    var getBtn=document.getElementById('method-get');
    var postBtn=document.getElementById('method-post');
    if(!getBtn||!postBtn) return;
    var active='bg-indigo-600 text-white';
    var idle='bg-white text-gray-700 hover:bg-gray-50';
    getBtn.className='px-4 py-2 text-sm font-medium focus:outline-none '+(requestMethod==='GET'?active:idle)+(settingsEditable?'':' opacity-60 cursor-not-allowed');
    postBtn.className='px-4 py-2 text-sm font-medium border-l border-gray-300 focus:outline-none '+(requestMethod==='POST'?active:idle)+(settingsEditable?'':' opacity-60 cursor-not-allowed');
    getBtn.disabled=!settingsEditable;
    postBtn.disabled=!settingsEditable;
    var bodySec=document.getElementById('post-body-section');
    if(bodySec){
        if(requestMethod==='POST') bodySec.classList.remove('hidden');
        else bodySec.classList.add('hidden');
    }
}
function renderHeaderRows(headers){
    var wrap=document.getElementById('header-rows');
    wrap.innerHTML='';
    if(!headers||!headers.length) headers=[{name:'',value:''}];
    headers.forEach(function(h){appendHeaderRow(h.name||'', h.value||'');});
}
function appendHeaderRow(name,value){
    var wrap=document.getElementById('header-rows');
    var row=document.createElement('div');
    row.className='flex items-center gap-2 header-row';
    var dis=settingsEditable?'':'disabled';
    row.innerHTML=
        '<input type="text" data-header="name" placeholder="Name (e.g. Authorization)" '+dis+' class="w-1/3 px-3 py-2 border border-gray-300 rounded-md text-sm font-mono focus:ring-2 focus:ring-indigo-500 focus:outline-none disabled:bg-gray-100 disabled:text-gray-500">'+
        '<input type="text" data-header="value" placeholder="Value" '+dis+' class="flex-1 px-3 py-2 border border-gray-300 rounded-md text-sm font-mono focus:ring-2 focus:ring-indigo-500 focus:outline-none disabled:bg-gray-100 disabled:text-gray-500">'+
        (settingsEditable?'<button type="button" onclick="removeHeaderRow(this)" class="px-2 py-2 text-gray-400 hover:text-red-600 text-lg leading-none" title="Remove">&times;</button>':'');
    wrap.appendChild(row);
    row.querySelector('[data-header="name"]').value=name||'';
    row.querySelector('[data-header="value"]').value=value||'';
}
window.addHeaderRow=function(){appendHeaderRow('','');};
window.removeHeaderRow=function(btn){
    var wrap=document.getElementById('header-rows');
    btn.closest('.header-row').remove();
    if(!wrap.querySelector('.header-row')) appendHeaderRow('','');
};
function collectHeaders(){
    var rows=document.querySelectorAll('#header-rows .header-row');
    var out=[];
    rows.forEach(function(row){
        var name=(row.querySelector('[data-header="name"]')||{}).value||'';
        var value=(row.querySelector('[data-header="value"]')||{}).value||'';
        name=name.trim();
        if(name) out.push({name:name,value:value});
    });
    return out;
}
function onBodyInput(presetVars){
    var body=document.getElementById('set-request-body').value;
    var names=[],seen={},m;
    bodyTplVarRe.lastIndex=0;
    while((m=bodyTplVarRe.exec(body))!==null){if(!seen[m[1]]){seen[m[1]]=true;names.push(m[1]);}}
    var sec=document.getElementById('body-tpl-section');
    if(names.length===0){sec.classList.add('hidden');bodyPrevVarNames=[];document.getElementById('body-tpl-seed-count').textContent='';return;}
    sec.classList.remove('hidden');
    var keep={};
    if(presetVars) keep=presetVars;
    else{
        bodyPrevVarNames.forEach(function(n){
            var inp=document.querySelector('#body-tpl-vars input[data-body-var="'+n+'"]');
            if(inp) keep[n]=inp.value;
        });
    }
    if(JSON.stringify(names)!==JSON.stringify(bodyPrevVarNames)){
        bodyPrevVarNames=names;
        renderBodyVarInputs(names, keep);
    }else if(presetVars){
        names.forEach(function(n){
            var inp=document.querySelector('#body-tpl-vars input[data-body-var="'+n+'"]');
            if(inp&&presetVars[n]) inp.value=presetVars[n];
        });
    }
    updateBodyPreview();
}
function renderBodyVarInputs(names, preset){
    var c=document.getElementById('body-tpl-vars');c.innerHTML='';
    names.forEach(function(name){
        var row=document.createElement('div');
        row.className='flex items-center gap-2';
        var val=(preset&&preset[name])?Mole.esc(preset[name]):'';
        var dis=settingsEditable?'':'disabled';
        row.innerHTML=
            '<span class="text-xs font-mono text-indigo-700 bg-white px-2 py-1 rounded border border-indigo-200 whitespace-nowrap min-w-[4rem] text-center">'+
            '{{'+Mole.esc(name)+'}}'+'</span>'+
            '<input type="text" data-body-var="'+Mole.esc(name)+'" placeholder="e.g. 1,hamster,5-10" value="'+val+'" '+dis+' '+
            'class="flex-1 px-2 py-1 text-sm border border-gray-300 rounded focus:outline-none focus:ring-1 focus:ring-indigo-500 disabled:bg-gray-100" '+
            'oninput="updateBodyPreview()">';
        c.appendChild(row);
    });
    updateBodyPreview();
}
function expandBodyValueExpr(expr){
    if(!expr||!expr.trim())return [];
    var parts=expr.split(','),result=[];
    parts.forEach(function(p){
        p=p.trim();if(!p)return;
        var rm=p.match(/^(\d+)-(\d+)$/);
        if(rm){var a=parseInt(rm[1],10),b=parseInt(rm[2],10);
            if(a<=b){for(var i=a;i<=b;i++)result.push(String(i));}
            else{for(var i=a;i>=b;i--)result.push(String(i));}
        }else{result.push(p);}
    });
    return result;
}
function updateBodyPreview(){
    var body=document.getElementById('set-request-body').value;
    var names=bodyPrevVarNames;
    var wrap=document.getElementById('body-tpl-preview-wrap');
    var countEl=document.getElementById('body-tpl-seed-count');
    if(names.length===0){wrap.classList.add('hidden');countEl.textContent='';return;}
    var vars={},anyFilled=false;
    names.forEach(function(n){
        var inp=document.querySelector('#body-tpl-vars input[data-body-var="'+n+'"]');
        var vals=inp?expandBodyValueExpr(inp.value):[];
        vars[n]=vals;if(vals.length>0)anyFilled=true;
    });
    if(!anyFilled){wrap.classList.add('hidden');countEl.textContent='';return;}
    var allFilled=names.every(function(n){return vars[n].length>0;});
    if(!allFilled){wrap.classList.add('hidden');countEl.textContent='';return;}
    var combos=[body];
    names.forEach(function(name){
        var next=[];
        combos.forEach(function(u){vars[name].forEach(function(val){next.push(u.split('{{'+name+'}}').join(val));});});
        combos=next;
    });
    var seedN=jobSeedCount||1;
    var total=seedN*combos.length;
    countEl.textContent=seedN+' seed URL'+(seedN!==1?'s':'')+' × '+combos.length+' payload'+(combos.length!==1?'s':'')+' = '+total+' request'+(total!==1?'s':'');
    wrap.classList.remove('hidden');
    var prev=document.getElementById('body-tpl-preview');
    var limit=20;
    var html=combos.slice(0,limit).map(function(u){return '<div class="whitespace-pre-wrap break-all">'+Mole.esc(u)+'</div>';}).join('');
    if(combos.length>limit){html+='<div class="text-gray-400 italic">... and '+(combos.length-limit)+' more</div>';}
    prev.innerHTML=html;
}
function toggleBodyPreview(){document.getElementById('body-tpl-preview').classList.toggle('hidden');}
function collectBodyVars(){
    if(!bodyPrevVarNames.length) return undefined;
    var out={},any=false;
    bodyPrevVarNames.forEach(function(n){
        var inp=document.querySelector('#body-tpl-vars input[data-body-var="'+n+'"]');
        if(inp&&inp.value.trim()){out[n]=inp.value.trim();any=true;}
    });
    return any?out:undefined;
}

function buildPayload(){
    var p={};
    var v;
    v=intVal('set-max-concurrent');if(v!==undefined)p.max_concurrent_requests=v;
    v=intVal('set-request-timeout');if(v!==undefined)p.request_timeout_seconds=v;
    v=intVal('set-politeness-delay');if(v!==undefined)p.politeness_delay_ms=v;
    v=intVal('set-max-depth');if(v!==undefined)p.max_depth=v;
    v=intVal('set-max-pages');if(v!==undefined)p.max_pages=v;
    v=intVal('set-max-pages-domain');if(v!==undefined)p.max_pages_per_domain=v;
    v=intVal('set-max-retries');if(v!==undefined)p.max_retries=v;
    v=strVal('set-user-agent');if(v!==undefined)p.user_agent=v;
    v=linesVal('set-url-include');if(v!==undefined)p.url_include_patterns=v;
    v=linesVal('set-url-exclude');if(v!==undefined)p.url_exclude_patterns=v;
    v=linesVal('set-extra-tracking');if(v!==undefined)p.extra_tracking_params=v;
    v=linesVal('set-skip-extensions');if(v!==undefined)p.skip_extensions=v;
    v=getTri('set-skip-content-dup');if(v!==undefined)p.skip_content_duplicates=v;
    v=getTri('set-respect-robots');if(v!==undefined)p.respect_robots_txt=v;
    applyFetchMode(p);
    v=strVal('set-headless-selector');if(v!==undefined)p.headless_wait_selector=v;
    v=getTri('set-enable-semantic');if(v!==undefined)p.enable_semantic_search=v;
    v=getTri('set-save-text-content');if(v!==undefined)p.save_text_content=v;
    v=document.getElementById('set-after-crawl-plugin').value;if(v!=='__default__')p.after_crawl_plugin=v;
    v=document.getElementById('set-after-job-plugin').value;if(v!=='__default__')p.after_job_plugin=v;
    v=getTri('set-enable-word-extraction');if(v!==undefined)p.enable_word_extraction=v;
    v=getTri('set-use-crawl-phrases-only');if(v!==undefined)p.use_crawl_phrases_only=v;
    v=getTri('set-stemming');if(v!==undefined)p.enable_stemming=v;
    v=getTri('set-lemmatization');if(v!==undefined)p.enable_lemmatization=v;
    v=document.getElementById('set-language').value;if(v)p.default_language=v;
    var methodEl=document.getElementById('set-request-method');
    requestMethod=(methodEl&&methodEl.value==='POST')||requestMethod==='POST'?'POST':'GET';
    if(requestMethod==='POST'){
        p.request_method='POST';
        var bodyEl=document.getElementById('set-request-body');
        if(bodyEl&&bodyEl.value!=='') p.request_body=bodyEl.value;
        var bodyVars=collectBodyVars();
        if(bodyVars)p.request_body_vars=bodyVars;
    }
    var headers=collectHeaders();
    if(headers.length)p.request_headers=headers;
    extendPayload(p);
    return p;
}
// showAlert reports the save result at the top of the form, next to the
// Save button and as a toast, so it is visible wherever the user scrolled.
// Errors stay until the next save; success messages fade.
function showAlert(msg,ok){
    var el=document.getElementById('settings-alert');
    el.textContent=msg;
    el.className='mb-4 p-3 rounded-md text-sm '+(ok?'bg-green-50 border border-green-200 text-green-700':'bg-red-50 border border-red-200 text-red-700');
    var st=document.getElementById('save-status');
    if(st){st.textContent=msg;st.title=msg;st.className='text-sm truncate max-w-md '+(ok?'text-green-700':'text-red-600 font-medium');}
    Mole.toast(msg,ok);
    if(ok) setTimeout(function(){el.classList.add('hidden');if(st)st.classList.add('hidden');},4000);
}
function clearFieldErrors(){
    document.querySelectorAll('.field-error').forEach(function(el){el.classList.remove('field-error');});
}
// markInvalid outlines an input or section and scrolls it into view.
function markInvalid(el,first){
    if(!el)return;
    el.classList.add('field-error');
    if(first){el.scrollIntoView({behavior:'smooth',block:'center'});if(el.focus)el.focus({preventScroll:true});}
}
// validateSettingsForm catches mistakes the server would reject, pointing at the exact input.
function validateSettingsForm(){
    var problems=[];
    var add=function(el,msg){markInvalid(el,problems.length===0);problems.push(msg);};
    document.querySelectorAll('#set-extract-fields .fe-row').forEach(function(row,i){
        var get=function(k){return row.querySelector(':scope > div [data-k="'+k+'"]');};
        var name=get('name'),sel=get('selector'),type=get('type').value,attr=get('attr');
        var nested=row.getAttribute('data-nested')==='1';
        var used=name.value.trim()||sel.value.trim()||attr.value.trim()||get('regex').value.trim();
        if(!used)return;
        var label=name.value.trim()?'"'+name.value.trim()+'"':'#'+(i+1);
        if(!name.value.trim())add(name,'Extraction field '+label+': name is required');
        if(!nested&&!sel.value.trim())add(sel,'Extraction field '+label+': selector is required');
        if(type==='attr'&&!attr.value.trim())add(attr,'Extraction field '+label+': attribute is required for type "attr"');
        if(type==='list'&&!row.querySelector(':scope > .fe-sub .fe-row'))add(get('type'),'Extraction field '+label+': add at least one item field to a list');
    });
    document.querySelectorAll('#set-actions .ae-row').forEach(function(row,i){
        var type=row.querySelector('[data-k="type"]').value,sel=row.querySelector('[data-k="selector"]'),val=row.querySelector('[data-k="value"]');
        if((type==='click'||type==='type'||type==='wait_for_selector')&&!sel.value.trim())add(sel,'Page action '+(i+1)+' ('+type+'): selector is required');
        if(type==='evaluate'&&!val.value.trim())add(val,'Page action '+(i+1)+': script is required');
    });
    document.querySelectorAll('#set-tag-rules .tr-row').forEach(function(row,i){
        var pat=row.querySelector('[data-k="pattern"]'),tag=row.querySelector('[data-k="tag"]');
        if(!pat.value.trim()&&!tag.value.trim())return;
        if(!pat.value.trim())add(pat,'Tag rule '+(i+1)+': URL pattern is required');
        else{try{new RegExp(pat.value.trim());}catch(e){add(pat,'Tag rule '+(i+1)+': invalid regex ('+e.message+')');}}
        if(!Mole.normalizeTag(tag.value))add(tag,'Tag rule '+(i+1)+': tag is required');
    });
    var wh=document.getElementById('wh-url');
    if(wh.value.trim()&&!/^https?:\/\/[^\s]+$/i.test(wh.value.trim()))add(wh,'Webhook URL must start with http:// or https://');
    return problems;
}
// highlightServerError points at the input a server-side validation message is about.
function highlightServerError(msg){
    var m=msg.toLowerCase(), target=null;
    var field=msg.match(/extraction field "([^".]+)/);
    if(field){
        document.querySelectorAll('#set-extract-fields [data-k="name"]').forEach(function(inp){if(inp.value.trim()===field[1]&&!target)target=inp.closest('.fe-row');});
        target=target||document.getElementById('set-extract-fields');
    }
    var rules=[['tag rule','set-tag-rules'],['keep tag','set-keep-tags'],['keep_tags','set-keep-tags'],['max_kept_pages','set-max-kept'],['include pattern','set-url-include'],['exclude pattern','set-url-exclude'],['extract_url_match','set-extract-match'],
        ['browser action','set-actions'],['proxy pool','set-proxy-pool'],['proxy','set-proxies'],['fingerprint','set-fingerprint'],
        ['host_rate_limit','set-host-rate'],['webhook','wh-url'],['limits','lim-pages'],['llm','set-llm-prompt'],
        ['payload variable','body-tpl-vars'],['resource type','set-block-resources'],['format','set-formats'],['plugin','set-after-crawl-plugin']];
    for(var i=0;!target&&i<rules.length;i++){if(m.indexOf(rules[i][0])>=0)target=document.getElementById(rules[i][1]);}
    markInvalid(target,true);
}

// settingsFormProblem clears earlier highlights and returns the first problem
// to fix before saving ("" when the form can be saved), outlining the inputs.
function settingsFormProblem(){
    clearFieldErrors();
    var problems=validateSettingsForm();
    if(problems.length) return problems.length===1?problems[0]:problems[0]+' (and '+(problems.length-1)+' more)';
    if(requestMethod==='POST' && bodyPrevVarNames.length){
        var missing=bodyPrevVarNames.filter(function(n){
            var inp=document.querySelector('#body-tpl-vars input[data-body-var="'+n+'"]');
            return !inp||!inp.value.trim();
        });
        if(missing.length){
            markInvalid(document.getElementById('body-tpl-vars'),true);
            return 'Fill values for payload variables: '+missing.map(function(n){return '{{'+n+'}}';}).join(', ');
        }
    }
    return '';
}
function resetToDefaults(){
    ['set-max-concurrent','set-request-timeout','set-politeness-delay','set-max-depth','set-max-pages','set-max-pages-domain','set-max-retries','set-user-agent',
     'set-url-include','set-url-exclude','set-extra-tracking','set-skip-extensions','set-headless-selector'].forEach(function(id){document.getElementById(id).value='';});
    ['set-skip-content-dup','set-respect-robots','set-enable-semantic','set-save-text-content','set-enable-word-extraction',
     'set-use-crawl-phrases-only','set-stemming','set-lemmatization'].forEach(function(id){setTri(id,undefined);});
    document.getElementById('set-language').value='';
    setFetchMode({});
    fillPluginSelect('set-after-crawl-plugin','after_crawl', undefined, 'after-crawl-plugin-desc', defaultSettings.after_crawl_plugin);
    fillPluginSelect('set-after-job-plugin','after_job', undefined, 'after-job-plugin-desc', defaultSettings.after_job_plugin);
    setRequestMethod('GET');
    renderHeaderRows([]);
    document.getElementById('set-request-body').value='';
    onBodyInput();
    populateExtra({});
}

var editors = {};

// Default / On / Off switch. "Default" (value "") omits the field so the
// engine default applies; its label shows what that default is.
function triDefault(el){
    var k=el.getAttribute('data-default-key');
    if(k) return !!defaultSettings[k];
    return el.getAttribute('data-default')==='true';
}
function renderTri(el){
    if(el.getAttribute('data-ready'))return;
    el.setAttribute('data-ready','1');
    el.innerHTML='<button type="button" data-v="" class="tri-default">Default · '+(triDefault(el)?'On':'Off')+'</button><button type="button" data-v="true">On</button><button type="button" data-v="false">Off</button>';
    el.querySelectorAll('button').forEach(function(b){
        b.disabled=!settingsEditable;
        b.addEventListener('click',function(){setTriValue(el,b.getAttribute('data-v'));});
    });
}
function setTriValue(el,v){
    el.setAttribute('data-value',v);
    el.querySelectorAll('button').forEach(function(b){b.classList.toggle('on',b.getAttribute('data-v')===v);});
}
function setTri(id, v){
    var el=document.getElementById(id);if(!el)return;
    renderTri(el);
    setTriValue(el,(v===null||v===undefined)?'':String(!!v));
}
function getTri(id){
    var el=document.getElementById(id);if(!el)return undefined;
    var v=el.getAttribute('data-value')||'';
    return v===''?undefined:v==='true';
}

/* ── Fetch mode: maps to use_headless_browser + headless_fallback ── */
function fetchModeOf(s){
    if(s.use_headless_browser===true) return 'browser';
    if(s.headless_fallback===true) return 'fallback';
    if(s.use_headless_browser===false||s.headless_fallback===false) return 'http';
    return '';
}
var fetchLabels={http:'Fast HTTP',fallback:'HTTP, browser if blocked',browser:'Headless browser'};
function setFetchMode(s){
    var mode=fetchModeOf(s||{});
    document.getElementById('fetch-default-label').textContent='Engine default: '+fetchLabels[fetchModeOf(defaultSettings)||'http'];
    document.querySelectorAll('input[name="fetch-mode"]').forEach(function(r){r.checked=r.value===mode;});
    syncFetchMode();
}
function currentFetchMode(){
    var r=document.querySelector('input[name="fetch-mode"]:checked');
    return r?r.value:'';
}
function applyFetchMode(p){
    switch(currentFetchMode()){
        case 'browser': p.use_headless_browser=true; break;
        case 'fallback': p.use_headless_browser=false; p.headless_fallback=true; break;
        case 'http': p.use_headless_browser=false; p.headless_fallback=false; break;
    }
}
// syncFetchMode dims browser options when pages will never reach the browser.
function syncFetchMode(){
    var mode=currentFetchMode()||fetchModeOf(defaultSettings)||'http';
    var box=document.getElementById('browser-options'), note=document.getElementById('browser-options-note');
    box.classList.toggle('opacity-50',mode==='http');
    note.textContent=mode==='browser'?'Used for every page':mode==='fallback'?'Used only for pages re-fetched in the browser':'Not used with Fast HTTP';
}
document.addEventListener('change',function(e){if(e.target&&e.target.name==='fetch-mode')syncFetchMode();});
function numVal(id){var v=document.getElementById(id).value.trim();return v===''?undefined:Number(v);}

function populateExtra(s){
    var ed=settingsEditable, d=defaultSettings||{};
    setVal('set-render-wait', s.headless_render_wait);
    editors.block=Mole.checkboxGroup(document.getElementById('set-block-resources'), catalog.block_resources, s.block_resources||[], {editable:ed});
    setTri('set-screenshot', s.screenshot);
    setTri('set-full-page', s.full_page_screenshot);
    editors.actions=Mole.actionsEditor(document.getElementById('set-actions'), s.browser_actions||[], {editable:ed, types:catalog.browser_actions});
    editors.formats=Mole.formatPicker(document.getElementById('set-formats'), catalog, s.formats||[], {editable:ed});
    setTri('set-only-main', s.only_main_content);
    editors.fields=Mole.fieldEditor(document.getElementById('set-extract-fields'), s.extract_fields||[], {editable:ed, types:catalog.field_types});
    setVal('set-extract-match', s.extract_url_match);
    setVal('set-llm-prompt', s.llm_prompt);
    setVal('set-llm-schema', s.llm_schema);
    var llmState=document.getElementById('llm-state');
    llmState.textContent=catalog.llm_enabled?('· model '+catalog.llm_model):'· not configured on this engine';
    llmState.className='text-xs '+(catalog.llm_enabled?'text-green-600':'text-amber-600');
    if(!catalog.llm_enabled){document.getElementById('set-llm-prompt').disabled=true;document.getElementById('set-llm-schema').disabled=true;}
    var pool=document.getElementById('set-proxy-pool');
    pool.innerHTML='<option value="">Default ('+(d.proxy_pool?d.proxy_pool:'automatic routing')+')</option><option value="auto">Automatic (routing rules)</option><option value="direct">Direct (no proxy)</option>'+
        (catalog.proxy_pools||[]).map(function(n){return '<option value="'+Mole.esc(n)+'">Pool: '+Mole.esc(n)+'</option>';}).join('');
    pool.value=s.proxy_pool||'';
    var fp=document.getElementById('set-fingerprint');
    fp.innerHTML='<option value="">Default</option>'+catalog.fingerprints.map(function(f){return '<option value="'+f+'">'+f+(f==='go'?' (no browser fingerprint)':'')+'</option>';}).join('');
    fp.value=s.tls_fingerprint||'';
    document.getElementById('set-proxies').value=(s.proxies||[]).join('\n');
    setTri('set-sticky-proxy', s.sticky_proxy);
    setVal('set-host-rate', s.host_rate_limit);
    document.getElementById('set-phrases').value=(s.phrases||[]).join('\n');
    setTri('set-change-detection', s.change_detection);
    setVal('set-compare-with', s.compare_with_job_id);
    editors.tagRules=Mole.tagRulesEditor(document.getElementById('set-tag-rules'), s.tag_rules||[], catalog, {editable:ed, onChange:function(){if(editors.keepTags)editors.keepTags.refresh();}});
    editors.keepTags=Mole.keepTagsPicker(document.getElementById('set-keep-tags'), s.keep_tags||[], catalog, {editable:ed, customTags:function(){
        return editors.tagRules.get().map(function(r){return r.tag;}).filter(Boolean);
    }});
    setVal('set-max-kept', s.max_kept_pages);
    document.getElementById('set-scope-domains').value=(s.scope_domains||[]).join('\n');
    populateGrouping(s, ed);

    var o=jobOptions||{};
    setVal('opt-priority', o.priority||0);
    var reg=document.getElementById('opt-region');
    var regions=(catalog.regions||[]).slice();
    if(o.region&&regions.indexOf(o.region)<0) regions.push(o.region);
    reg.innerHTML='<option value="">Any worker</option><option value="auto">Automatic (from proxy routes)</option>'+regions.map(function(r){return '<option value="'+Mole.esc(r)+'">'+Mole.esc(r)+'</option>';}).join('');
    reg.value=o.region||'';
    var l=o.limits||{};
    setVal('lim-pages', l.max_pages); setVal('lim-requests', l.max_requests);
    setVal('lim-mb', l.max_bytes?Math.round(l.max_bytes/1048576):undefined);
    setVal('lim-browser', l.max_browser_seconds); setVal('lim-llm', l.max_llm_tokens);
    setVal('lim-duration', l.max_duration_seconds?Math.round(l.max_duration_seconds/60):undefined);
    var w=o.webhook||{};
    setVal('wh-url', w.url); setVal('wh-secret', w.secret);
    editors.events=Mole.checkboxGroup(document.getElementById('wh-events'), catalog.webhook_events, w.events||[], {editable:ed});
}

function extendPayload(p){
    var v;
    v=numVal('set-render-wait'); if(v!==undefined)p.headless_render_wait=v;
    v=editors.block.get(); if(v.length)p.block_resources=v;
    v=getTri('set-screenshot'); if(v!==undefined)p.screenshot=v;
    v=getTri('set-full-page'); if(v!==undefined)p.full_page_screenshot=v;
    v=editors.actions.get(); if(v.length)p.browser_actions=v;
    v=editors.formats.get(); if(v.length)p.formats=v;
    v=getTri('set-only-main'); if(v!==undefined)p.only_main_content=v;
    v=editors.fields.get(); if(v.length)p.extract_fields=v;
    v=strVal('set-extract-match'); if(v!==undefined)p.extract_url_match=v;
    v=strVal('set-llm-prompt'); if(v!==undefined)p.llm_prompt=v;
    v=strVal('set-llm-schema'); if(v!==undefined)p.llm_schema=v;
    v=document.getElementById('set-proxy-pool').value; if(v)p.proxy_pool=v;
    v=document.getElementById('set-fingerprint').value; if(v)p.tls_fingerprint=v;
    v=linesVal('set-proxies'); if(v!==undefined)p.proxies=v;
    v=getTri('set-sticky-proxy'); if(v!==undefined)p.sticky_proxy=v;
    v=numVal('set-host-rate'); if(v!==undefined)p.host_rate_limit=v;
    v=linesVal('set-phrases'); if(v!==undefined)p.phrases=v;
    v=getTri('set-change-detection'); if(v!==undefined)p.change_detection=v;
    v=strVal('set-compare-with'); if(v!==undefined)p.compare_with_job_id=v;
    v=editors.tagRules.get(); if(v.length)p.tag_rules=v;
    v=editors.keepTags.get(); if(v.length)p.keep_tags=v;
    v=intVal('set-max-kept'); if(v!==undefined&&v>0)p.max_kept_pages=v;
    v=linesVal('set-scope-domains'); if(v!==undefined)p.scope_domains=v;
    if(document.getElementById('set-group-products').checked){
        p.group_products=true;
        var fields=compareFieldsValue(); if(fields.length)p.compare_fields=fields;
        p.group_by_variant=document.getElementById('set-group-by-variant').checked;
        p.match_threshold=parseFloat(document.getElementById('set-match-threshold').value);
        p.match_mode=document.getElementById('set-match-mode').value;
    }
}

/* ── Group & compare ──
   The field picker offers the engine's detected product fields (from
   /v1/info) plus whatever extract fields this job defines, since those are
   read from each page's extracted JSON. */
function groupingFieldChoices(){
    var out=(catalog.compare_fields||[]).slice();
    var seen={}; out.forEach(function(f){seen[f.name]=true;});
    (editors.fields?editors.fields.get():[]).forEach(function(f){
        if(f.name&&!seen[f.name]){ seen[f.name]=true; out.push({name:f.name,label:f.name,kind:'extracted'}); }
    });
    return out;
}
function renderCompareFields(chosen, editable){
    var box=document.getElementById('set-compare-fields');
    if(!box) return;
    box.innerHTML=groupingFieldChoices().map(function(f){
        var on=chosen.indexOf(f.name)>=0;
        return '<label class="flex items-center gap-2 text-sm text-gray-700">'+
            '<input type="checkbox" data-compare-field="'+Mole.esc(f.name)+'"'+(on?' checked':'')+(editable?'':' disabled')+'> '+
            Mole.esc(f.label)+' <span class="text-xs text-gray-400">'+Mole.esc(f.kind)+'</span></label>';
    }).join('');
}
function compareFieldsValue(){
    return Array.prototype.map.call(document.querySelectorAll('[data-compare-field]:checked'), function(el){
        return el.getAttribute('data-compare-field');
    });
}
function syncGrouping(){
    var on=document.getElementById('set-group-products').checked;
    document.getElementById('grouping-opts').classList.toggle('hidden', !on);
    // Name strictness only means something when names are matched at all.
    var mode=document.getElementById('set-match-mode');
    var wrap=document.getElementById('set-threshold-wrap');
    if(mode&&wrap) wrap.classList.toggle('hidden', mode.value!=='auto');
}
function populateGrouping(s, editable){
    var on=s.group_products===true;
    var box=document.getElementById('set-group-products');
    if(!box) return;
    box.checked=on;
    box.addEventListener('change', syncGrouping);
    renderCompareFields(s.compare_fields&&s.compare_fields.length?s.compare_fields:(catalog.compare_fields?['price','regular_price','availability']:[]), editable);
    document.getElementById('set-group-by-variant').checked=s.group_by_variant!==false;
    var mode=document.getElementById('set-match-mode');
    mode.value=s.match_mode||'auto';
    mode.addEventListener('change', syncGrouping);
    var th=document.getElementById('set-match-threshold');
    th.value=s.match_threshold!=null?s.match_threshold:0.72;
    document.getElementById('set-match-threshold-val').textContent=th.value;
    th.addEventListener('input', function(){ document.getElementById('set-match-threshold-val').textContent=this.value; });
    syncGrouping();
}

function collectOptions(){
    var l={};
    var put=function(k,v){if(v!==undefined&&v>0)l[k]=v;};
    put('max_pages',numVal('lim-pages')); put('max_requests',numVal('lim-requests'));
    var mb=numVal('lim-mb'); put('max_bytes',mb!==undefined?Math.round(mb*1048576):undefined);
    put('max_browser_seconds',numVal('lim-browser')); put('max_llm_tokens',numVal('lim-llm'));
    var mins=numVal('lim-duration'); put('max_duration_seconds',mins!==undefined?Math.round(mins*60):undefined);
    var webhook={url:(document.getElementById('wh-url').value||'').trim(),secret:document.getElementById('wh-secret').value,events:editors.events.get()};
    return {priority:numVal('opt-priority')||0, region:document.getElementById('opt-region').value, limits:l, webhook:webhook};
}
