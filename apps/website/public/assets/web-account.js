// 宇海用户中心。只请求 https://api.yhvpn.io，会话放在 HttpOnly Cookie 里。
(function (root) {
  'use strict';

  var API_ORIGIN = 'https://api.yhvpn.io';

  function apiBase(doc) {
    var meta = doc.querySelector('meta[name="yuhai-api"]');
    var declared = meta && meta.content ? String(meta.content).trim().replace(/\/$/, '') : API_ORIGIN;
    if (declared !== API_ORIGIN) return '';
    return API_ORIGIN;
  }

  function show(el, text) {
    if (!el) return;
    el.hidden = !text;
    el.textContent = text || '';
  }

  function pad(n) { return (n < 10 ? '0' : '') + n; }

  function formatWhen(iso, serverNow) {
    if (!iso) return '未开通';
    var d = new Date(iso);
    if (isNaN(d.getTime())) return '未开通';
    var text = d.getFullYear() + '-' + pad(d.getMonth() + 1) + '-' + pad(d.getDate()) + ' ' + pad(d.getHours()) + ':' + pad(d.getMinutes());
    var now = serverNow ? new Date(serverNow).getTime() : Date.now();
    if (d.getTime() <= now) return text + '（已到期）';
    return text;
  }

  function boot() {
    var doc = root.document;
    var loading = doc.getElementById('account-loading');
    var gate = doc.getElementById('account-gate');
    var panel = doc.getElementById('account-panel');
    if (!panel) return;
    var base = apiBase(doc);
    if (base !== API_ORIGIN) {
      if (loading) loading.textContent = '账户接口不可用，请刷新页面后再试';
      return;
    }

    var addressEl = doc.getElementById('acc-address');
    var planEl = doc.getElementById('acc-plan');
    var expireEl = doc.getElementById('acc-expire');
    var devicesEl = doc.getElementById('acc-devices');
    var trialBtn = doc.getElementById('trial-claim');
    var trialCopy = doc.getElementById('trial-copy');
    var trialError = doc.getElementById('trial-error');
    var trialOk = doc.getElementById('trial-ok');
    var redeemForm = doc.getElementById('redeem-form');
    var redeemCode = doc.getElementById('redeem-code');
    var redeemError = doc.getElementById('redeem-error');
    var redeemOk = doc.getElementById('redeem-ok');
    var redeemSubmit = doc.getElementById('redeem-submit');
    var logoutBtn = doc.getElementById('logout');

    function paint(data) {
      if (loading) loading.hidden = true;
      if (gate) gate.hidden = true;
      panel.hidden = false;
      if (addressEl) addressEl.textContent = data.account || '—';
      if (planEl) planEl.textContent = data.plan_name || '未开通';
      if (expireEl) expireEl.textContent = data.expire_at ? formatWhen(data.expire_at, data.server_time) : '未开通';
      if (devicesEl) {
        var count = typeof data.device_count === 'number' ? data.device_count : 0;
        var limit = typeof data.device_limit === 'number' ? data.device_limit : 0;
        devicesEl.textContent = limit > 0 ? (count + ' / ' + limit + ' 台') : (count + ' 台');
      }
      if (trialBtn) {
        trialBtn.hidden = !data.trial_eligible;
        trialBtn.disabled = false;
      }
      if (trialCopy && !data.trial_eligible) {
        trialCopy.textContent = data.active ? '试用或套餐已经在生效，不能再领一次。' : '这个账户现在不能领取试用。';
      }
    }

    function load() {
      return fetch(base + '/v1/auth/web/account', { method: 'GET', mode: 'cors', credentials: 'include' }).then(function (res) {
        return res.json().then(function (body) { return { status: res.status, body: body }; }, function () { return { status: res.status, body: null }; });
      }).then(function (res) {
        if (res.status === 401) {
          if (loading) loading.hidden = true;
          if (panel) panel.hidden = true;
          if (gate) gate.hidden = false;
          return null;
        }
        if (!res.body || !res.body.data) {
          throw new Error((res.body && res.body.error && res.body.error.message) || '账户暂时读不出来，请稍后再试');
        }
        paint(res.body.data);
        return res.body.data;
      });
    }

    load().catch(function (err) {
      if (loading) loading.textContent = (err && err.message) || '网络暂时连不上，请稍后再试';
    });

    if (trialBtn) {
      trialBtn.addEventListener('click', function () {
        if (trialBtn.disabled) return;
        trialBtn.disabled = true;
        show(trialError, '');
        show(trialOk, '');
        fetch(base + '/v1/auth/web/trial', {
          method: 'POST',
          mode: 'cors',
          credentials: 'include',
          headers: { 'Content-Type': 'application/json' },
          body: '{}'
        }).then(function (res) {
          return res.json().then(function (body) { return { status: res.status, body: body }; }, function () { return { status: res.status, body: null }; });
        }).then(function (res) {
          if (!res.body || res.status !== 200 || !res.body.data) {
            throw new Error((res.body && res.body.error && res.body.error.message) || '试用没有领到，请稍后再试');
          }
          show(trialOk, res.body.data.status === 'already' ? '这个账户已经领过试用。' : '已开通 24 小时免费试用。');
          return load();
        }).catch(function (err) {
          show(trialError, (err && err.message) || '网络暂时连不上，请稍后再试');
          trialBtn.disabled = false;
        });
      });
    }

    if (redeemForm) {
      redeemForm.addEventListener('submit', function (event) {
        event.preventDefault();
        if (redeemSubmit && redeemSubmit.disabled) return;
        var code = redeemCode ? redeemCode.value.trim() : '';
        show(redeemError, '');
        show(redeemOk, '');
        if (!code) {
          show(redeemError, '请填写兑换码');
          return;
        }
        if (redeemSubmit) redeemSubmit.disabled = true;
        fetch(base + '/v1/auth/web/redeem', {
          method: 'POST',
          mode: 'cors',
          credentials: 'include',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ code: code })
        }).then(function (res) {
          return res.json().then(function (body) { return { status: res.status, body: body }; }, function () { return { status: res.status, body: null }; });
        }).then(function (res) {
          if (!res.body || res.status !== 200 || !res.body.data) {
            throw new Error((res.body && res.body.error && res.body.error.message) || '兑换没有完成，请稍后再试');
          }
          var name = res.body.data.card_name || '套餐';
          show(redeemOk, '已开通' + name + '。');
          if (redeemCode) redeemCode.value = '';
          return load();
        }).catch(function (err) {
          show(redeemError, (err && err.message) || '网络暂时连不上，请稍后再试');
        }).then(function () {
          if (redeemSubmit) redeemSubmit.disabled = false;
        });
      });
    }

    if (logoutBtn) {
      logoutBtn.addEventListener('click', function () {
        logoutBtn.disabled = true;
        fetch(base + '/v1/auth/web/logout', {
          method: 'POST',
          mode: 'cors',
          credentials: 'include',
          headers: { 'Content-Type': 'application/json' },
          body: '{}'
        }).then(function (res) {
          if (!res.ok) throw new Error('logout');
          root.location.assign('/login/');
        }).catch(function () {
          logoutBtn.disabled = false;
          logoutBtn.textContent = '退出没有完成，请再试一次';
        });
      });
    }
  }

  if (root.document && root.document.getElementById && root.document.getElementById('account-panel')) boot();
})(typeof globalThis !== 'undefined' ? globalThis : this);
