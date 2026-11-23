/* ===== DSH-OVERVIEW: 统一总览页逻辑 =====
   目标：一屏聚合「账号总数 / 签到完成率 / 两平台积分 / 模型数」+ 账号卡列表。
   数据源（已有接口，无需改后端）：
     /panel/api/overview                  → 账号/健康/冷却等运行时状态
     /panel/api/trae/accounts             → Trae 账号（含签到、积分、到期）
     /panel/api/ide/workbuddy/accounts    → WorkBuddy 账号
     /panel/api/models                    → 模型目录
*/

(function () {
  'use strict';

  const API = {
    overview: 'overview',
    trae: 'trae/accounts',
    wb: 'ide/workbuddy/accounts',
    models: 'models',
  };

  // 面板的密钥存在 localStorage['wb2api.key']，且 app.js 里有统一的 api() 封装
  // （自动带 Bearer、401 时弹密钥框、合并同路径在途请求）。这里直接复用，
  // 避免自己拼 localStorage 键名出错。
  const LS_KEY = 'wb2api.key';

  function apiKey() {
    try {
      return localStorage.getItem(LS_KEY) || '';
    } catch (_) {
      return '';
    }
  }

  // path 用相对形式（如 'trae/accounts'），与 app.js 的 api() 约定一致。
  async function get(path, opts) {
    if (typeof window.api === 'function') {
      return window.api(path, opts || {});
    }
    // 兜底：window.api 不可用（理论上不会，app.js 是普通脚本，函数在全局作用域）
    const k = apiKey();
    const h = {};
    if (k && /^[\x00-\xff]*$/.test(k)) h['Authorization'] = 'Bearer ' + k;
    const r = await fetch('/panel/api/' + path, { headers: h, cache: 'no-store' });
    if (r.status === 401) throw new Error('密钥无效或未填写');
    const d = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
    return d;
  }

  const fmt = (n, d = 2) => {
    if (n === null || n === undefined || n === '') return '—';
    const v = Number(n);
    if (!isFinite(v)) return String(n);
    return v.toLocaleString('zh-CN', { minimumFractionDigits: 0, maximumFractionDigits: d });
  };

  // 状态：平台筛选 + 签到筛选
  const state = { plat: 'all', st: 'all', data: null };

  function el(tag, cls, text) {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined) e.textContent = text;
    return e;
  }

  // ---- 顶部四张 KPI ----
  function renderKpis(root, trae, wb) {
    const all = [...trae, ...wb];
    const totalAcct = all.length;
    const signedTrae = trae.filter(a => a.checked_in).length;
    const signedWb = wb.filter(a => a.checked_in_today).length;
    const signed = signedTrae + signedWb;
    const pct = totalAcct ? Math.round((signed / totalAcct) * 100) : 0;

    const traeCredits = trae.reduce((s, a) => s + (Number(a.credits) || 0), 0);
    const wbCredits = wb.reduce((s, a) => s + (Number(a.credits) || 0), 0);

    const kpis = [
      {
        lbl: '托管账号总数',
        num: totalAcct,
        unit: '个',
        sub: `Trae ${trae.length} · WorkBuddy ${wb.length}`,
        cls: '',
      },
      {
        lbl: '今日签到完成率',
        num: pct,
        unit: '%',
        sub: `${signed}/${totalAcct} 已签`,
        cls: pct === 100 ? 'ok' : (pct > 0 ? 'warn' : ''),
        bar: pct,
      },
      {
        lbl: 'Trae 剩余积分',
        num: fmt(traeCredits),
        sub: `${trae.length} 个账号合计`,
        cls: '',
      },
      {
        lbl: 'WorkBuddy 剩余积分',
        num: fmt(wbCredits),
        sub: `${wb.length} 个账号合计`,
        cls: '',
      },
    ];

    const box = el('div', 'ov-kpis');
    kpis.forEach(k => {
      const c = el('div', 'ov-kpi ' + (k.cls || ''));
      c.appendChild(el('div', 'lbl', k.lbl));
      const n = el('div', 'num', String(k.num));
      if (k.unit) {
        const u = el('small', '', k.unit);
        n.appendChild(u);
      }
      c.appendChild(n);
      if (k.bar !== undefined) {
        const bar = el('div', 'ov-bar');
        const i = el('i');
        i.style.width = k.bar + '%';
        bar.appendChild(i);
        c.appendChild(bar);
      }
      c.appendChild(el('div', 'sub', k.sub));
      box.appendChild(c);
    });
    root.appendChild(box);
  }

  // ---- 平台授权卡 ----
  function renderPlats(root, trae, wb, models) {
    const box = el('div', 'ov-plats');
    const items = [
      { name: 'WorkBuddy', cnt: wb.length, desc: '每日签到 · 积分与成长任务', way: 'OAuth 账号池' },
      { name: 'Trae', cnt: trae.length, desc: '每日签到 · 积分与自动续期', way: 'Web 凭证池' },
      { name: '模型目录', cnt: models.length, desc: 'trae: / cn: 双平台模型', way: '已同步' },
    ];
    items.forEach(it => {
      const c = el('div', 'ov-plat');
      const h = el('h4');
      h.appendChild(el('span', '', it.name));
      h.appendChild(el('span', 'cnt', String(it.cnt)));
      c.appendChild(h);
      c.appendChild(el('div', 'desc', it.desc));
      c.appendChild(el('div', 'way', it.way));
      box.appendChild(c);
    });
    root.appendChild(box);
  }

  // ---- 账号卡 ----
  function acctCard(a, plat) {
    const isTrae = plat === 'trae';
    const name = isTrae ? (a.name || a.user_id) : (a.nickname || a.uid);
    const uid = isTrae ? a.user_id : a.uid;
    const credits = a.credits;
    const total = isTrae ? null : a.credits_total;
    const signed = isTrae ? !!a.checked_in : !!a.checked_in_today;
    const expiry = isTrae ? a.expires_at : a.credits_earliest_expiry;

    const card = el('div', 'ov-acct');

    const hd = el('div', 'hd');
    const left = el('div');
    const nm = el('div', 'nm');
    nm.appendChild(el('span', '', name));
    nm.appendChild(document.createTextNode(' '));
    const tag = el('span', 'ov-badge ' + (isTrae ? 'tr' : ''));
    tag.textContent = isTrae ? 'TR' : 'WB';
    tag.style.background = isTrae ? 'var(--accent-soft)' : 'rgba(26,158,92,.14)';
    tag.style.color = isTrae ? 'var(--accent)' : '#1a9e5c';
    nm.appendChild(tag);
    left.appendChild(nm);
    left.appendChild(el('div', 'uid', uid || ''));
    hd.appendChild(left);

    const badge = el('span', 'ov-badge ' + (signed ? 'done' : 'todo'));
    badge.textContent = signed ? '已签到' : '待签到';
    hd.appendChild(badge);
    card.appendChild(hd);

    const rows = el('div', 'rows');
    const mk = (k, v) => {
      const r = el('div', 'r');
      r.appendChild(el('span', 'k', k));
      r.appendChild(el('span', 'v', v));
      rows.appendChild(r);
    };
    mk('剩余积分', fmt(credits));
    if (!isTrae && a.credits_earliest_remaining !== undefined && a.credits_earliest_remaining !== null) {
      mk('可用(最早到期)', fmt(a.credits_earliest_remaining));
    }
    if (total !== null && total !== undefined) mk('积分总额', fmt(total));
    if (expiry) mk('到期', String(expiry).replace('T', ' ').slice(0, 16));
    card.appendChild(rows);

    const acts = el('div', 'acts');
    const b1 = el('button', signed ? '' : 'pri', signed ? '刷新' : '签到');
    b1.disabled = signed;
    const b2 = el('button', '', '续期');
    acts.appendChild(b1);
    acts.appendChild(b2);
    card.appendChild(acts);

    // 事件：签到 / 续期
    b1.addEventListener('click', async () => {
      b1.disabled = true;
      b1.textContent = '…';
      try {
        const path = isTrae ? 'trae/checkin' : 'ide/workbuddy/checkin';
        await get(path, { method: 'POST', body: JSON.stringify(isTrae ? { user_id: uid } : { uid: uid }) });
        b1.textContent = '已签到';
        badge.textContent = '已签到';
        badge.className = 'ov-badge done';
      } catch (e) {
        b1.textContent = '失败';
        b1.disabled = false;
      }
    });

    b2.addEventListener('click', async () => {
      b2.disabled = true;
      b2.textContent = '…';
      try {
        const path = isTrae ? 'trae/refresh' : 'ide/workbuddy/renew';
        await get(path, { method: 'POST', body: JSON.stringify(isTrae ? { user_id: uid } : { uid: uid }) });
        b2.textContent = '已续期';
      } catch (e) {
        b2.textContent = '失败';
        b2.disabled = false;
      }
    });

    return card;
  }

  // ---- 列表 + 筛选 ----
  function renderList(root, trae, wb) {
    let items = [];
    if (state.plat === 'all' || state.plat === 'trae') {
      items = items.concat(trae.map(a => ({ a, plat: 'trae' })));
    }
    if (state.plat === 'all' || state.plat === 'wb') {
      items = items.concat(wb.map(a => ({ a, plat: 'wb' })));
    }

    const isSigned = x => x.plat === 'trae' ? !!x.a.checked_in : !!x.a.checked_in_today;
    if (state.st === 'done') items = items.filter(isSigned);
    else if (state.st === 'todo') items = items.filter(x => !isSigned(x));

    const tools = el('div', 'ov-tools');
    const ttl = el('div', 'ttl');
    ttl.appendChild(document.createTextNode('托管账号列表'));
    ttl.appendChild(el('span', '', `共 ${items.length} 个`));
    tools.appendChild(ttl);

    const plats = [['all', '全部'], ['trae', 'Trae'], ['wb', 'WorkBuddy']];
    plats.forEach(([v, label]) => {
      const c = el('button', 'ov-chip' + (state.plat === v ? ' on' : ''), label);
      c.addEventListener('click', () => { state.plat = v; rerender(root); });
      tools.appendChild(c);
    });

    const sts = [['all', '全部'], ['done', '今日已签'], ['todo', '今日未签']];
    sts.forEach(([v, label]) => {
      const c = el('button', 'ov-chip' + (state.st === v ? ' on' : ''), label);
      c.addEventListener('click', () => { state.st = v; rerender(root); });
      tools.appendChild(c);
    });
    root.appendChild(tools);

    const list = el('div', 'ov-list');
    if (!items.length) {
      list.appendChild(el('div', 'ov-empty', '没有符合条件的账号'));
    } else {
      items.forEach(({ a, plat }) => list.appendChild(acctCard(a, plat)));
    }
    root.appendChild(list);
  }

  let mountPoint = null;

  function rerender(host) {
    if (!mountPoint) return;
    mountPoint.innerHTML = '';
    render(host);
  }

  function render(host) {
    const { trae, wb, models } = state.data;
    renderKpis(mountPoint, trae, wb);
    renderPlats(mountPoint, trae, wb, models);
    renderList(mountPoint, trae, wb);
  }

  // show()：进入页面时调用。已有数据则立刻渲染（避免每次切页都重新拉+白屏），
  // 同时后台静默刷新；无数据才显示 loading。
  function show() {
    const host = document.getElementById('view-overview');
    if (!host) return;
    if (!mountPoint || !host.contains(mountPoint)) {
      host.innerHTML = '';
      mountPoint = el('div', 'ov-wrap');
      host.appendChild(mountPoint);
    }
    if (state.data) {
      mountPoint.innerHTML = '';
      render(host);
      // 静默刷新，失败不影响已渲染内容
      load(true).catch(() => {});
      return;
    }
    mountPoint.innerHTML = '';
    mountPoint.appendChild(el('div', 'ov-empty', '正在加载…'));
    load(false).catch(() => {});
  }

  async function load(silent) {
    const host = document.getElementById('view-overview');
    if (!host) return;

    if (!mountPoint || !host.contains(mountPoint)) {
      host.innerHTML = '';
      mountPoint = el('div', 'ov-wrap');
      host.appendChild(mountPoint);
    }
    if (!silent) {
      mountPoint.innerHTML = '';
      mountPoint.appendChild(el('div', 'ov-empty', '正在加载…'));
    }

    try {
      const [traeRes, wbRes, modelsRes, ovRes] = await Promise.all([
        get(API.trae),
        get(API.wb),
        get(API.models),
        get(API.overview).catch(() => ({ accounts: [] })),
      ]);

      // WB 的积分不在 /ide/workbuddy/accounts 里，只有 /overview 才有。
      // 按 uid 合并：以 overview 为准补齐 credits / credits_total / 到期 / 签到。
      const ovByUid = new Map(
        (ovRes.accounts || []).map(a => [a.uid, a])
      );
      const wb = (wbRes.accounts || []).map(a => {
        const o = ovByUid.get(a.uid) || {};
        return Object.assign({}, a, {
          credits: o.credits,
          credits_total: o.credits_total,
          credits_earliest_expiry: o.credits_earliest_expiry,
          credits_earliest_remaining: o.credits_earliest_remaining,
          checked_in_today: a.checked_in_today !== undefined
            ? a.checked_in_today
            : !!o.checkin_done,
          cooling: o.cooling,
          disabled: o.disabled,
        });
      });

      state.data = {
        trae: traeRes.accounts || [],
        wb: wb,
        models: modelsRes.models || [],
      };
      mountPoint.innerHTML = '';
      render(host);
    } catch (e) {
      if (silent && state.data) return;   // 静默刷新失败：保留旧内容
      mountPoint.innerHTML = '';
      mountPoint.appendChild(el('div', 'ov-empty', '加载失败：' + e.message));
    }
  }

  // 暴露给面板路由
  window.__dshOverview = {
    show,
    load: () => load(false),
    rerender: () => rerender(null),
  };
})();
