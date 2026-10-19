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

    var paying = false;
    var pollTimer = 0;
    var tickTimer = 0;
    var clockSkew = 0;
    var planList = doc.getElementById('plan-list');
    var buyError = doc.getElementById('buy-error');
    var payPanel = doc.getElementById('pay-panel');
    var payAmount = doc.getElementById('pay-amount');
    var payAddress = doc.getElementById('pay-address');
    var payCountdown = doc.getElementById('pay-countdown');
    var payStatus = doc.getElementById('pay-status');
    var payOk = doc.getElementById('pay-ok');
    var payQr = doc.getElementById('pay-qr');
    var copyAmount = doc.getElementById('pay-copy-amount');
    var copyAddress = doc.getElementById('pay-copy-address');
    var cancelOrder = doc.getElementById('pay-cancel');

    function uuid() {
      if (root.crypto && root.crypto.randomUUID) return root.crypto.randomUUID();
      var bytes = new Uint8Array(16);
      root.crypto.getRandomValues(bytes);
      bytes[6] = (bytes[6] & 15) | 64;
      bytes[8] = (bytes[8] & 63) | 128;
      var hex = [];
      for (var i = 0; i < 16; i++) hex.push((bytes[i] + 256).toString(16).slice(1));
      return hex.slice(0, 4).join('') + '-' + hex.slice(4, 6).join('') + '-' + hex.slice(6, 8).join('') + '-' + hex.slice(8, 10).join('') + '-' + hex.slice(10).join('');
    }

    function api(path, method, body) {
      return fetch(base + path, {
        method: method,
        mode: 'cors',
        credentials: 'include',
        headers: body ? { 'Content-Type': 'application/json' } : {},
        body: body ? JSON.stringify(body) : undefined
      }).then(function (res) {
        return res.json().then(function (payload) { return { status: res.status, body: payload }; }, function () { return { status: res.status, body: null }; });
      });
    }

    function moneyText(value) {
      var raw = String(value || '');
      if (raw.indexOf('.') < 0) return raw + '.00';
      raw = raw.replace(/0+$/, '').replace(/\.$/, '');
      var dot = raw.indexOf('.');
      if (dot < 0) return raw + '.00';
      if (raw.length - dot - 1 < 2) return raw + '0';
      return raw;
    }

    function planTitle(plan) {
      var name = plan.name || '套餐';
      var days = plan.duration_days;
      if (days === 30 && name.indexOf('月') < 0) return name + ' · 月卡';
      if (days === 90 && name.indexOf('季') < 0) return name + ' · 季卡';
      if (days === 365 && name.indexOf('年') < 0) return name + ' · 年卡';
      if (days) return name + ' · ' + days + ' 天';
      return name;
    }

    function drawQR(rows) {
      if (!payQr) return;
      while (payQr.firstChild) payQr.removeChild(payQr.firstChild);
      if (!rows || !rows.length) return;
      var grid = doc.createElement('div');
      grid.className = 'pay-qr';
      grid.style.setProperty('--n', String(rows.length));
      grid.setAttribute('role', 'img');
      grid.setAttribute('aria-label', '收款地址二维码');
      for (var y = 0; y < rows.length; y++) {
        var line = String(rows[y]);
        for (var x = 0; x < line.length; x++) {
          var cell = doc.createElement('i');
          if (line.charAt(x) === '1') cell.className = 'on';
          grid.appendChild(cell);
        }
      }
      payQr.appendChild(grid);
    }

    function stopPay() {
      if (pollTimer) root.clearInterval(pollTimer);
      if (tickTimer) root.clearInterval(tickTimer);
      pollTimer = 0;
      tickTimer = 0;
    }

    function showOrder(order) {
      paying = true;
      if (planList) planList.hidden = true;
      if (payPanel) payPanel.hidden = false;
      show(buyError, '');
      var display = order.pay_display || moneyText(order.pay_amount);
      if (payAmount) payAmount.textContent = display + ' USDT';
      if (payAddress) payAddress.textContent = order.receive_address || '';
      drawQR(order.qr_modules);
      if (order.server_time) clockSkew = new Date(order.server_time).getTime() - Date.now();
      function tick() {
        if (!order.expires_at || !payCountdown) return;
        var left = new Date(order.expires_at).getTime() - (Date.now() + clockSkew);
        if (order.status !== 'PENDING') {
          payCountdown.textContent = '';
          return;
        }
        if (left <= 0) {
          payCountdown.textContent = '订单已到时间。如果已经转账，到账后仍会自动开通。';
          return;
        }
        var total = Math.floor(left / 1000);
        var m = Math.floor(total / 60);
        var s = total % 60;
        payCountdown.textContent = '请在 ' + m + ' 分 ' + (s < 10 ? '0' : '') + s + ' 秒内完成转账';
      }
      tick();
      if (tickTimer) root.clearInterval(tickTimer);
      tickTimer = root.setInterval(tick, 1000);
      var statusText = '';
      if (order.status === 'COMPLETED') statusText = '';
      else if (order.status === 'CONFIRMING' || order.status === 'PAID') statusText = '已检测到转账，正在确认并开通…';
      else if (order.status === 'EXPIRED') statusText = '订单已过期。如果已经转账，到账后仍会自动开通。';
      else if (order.status === 'CANCELLED') statusText = '订单已取消。';
      else if (order.status_hint === 'EXCEPTION_REVIEW') statusText = '金额没有对上，已转人工核对。请保留交易哈希。';
      if (payStatus) payStatus.textContent = statusText;
      if (cancelOrder) {
        cancelOrder.hidden = order.status !== 'PENDING';
        cancelOrder.dataset.order = order.order_no || '';
      }
      if (order.status === 'COMPLETED' || order.status === 'CANCELLED') {
        stopPay();
        paying = false;
        if (order.status === 'COMPLETED') show(payOk, '已开通。');
        if (order.status === 'CANCELLED' && payPanel) payPanel.hidden = true;
        if (planList) planList.hidden = false;
        if (order.status === 'COMPLETED') load();
        else loadPlans();
      }
    }

    function watchOrder(orderNo) {
      if (pollTimer) root.clearInterval(pollTimer);
      function pull() {
        api('/v1/auth/web/orders/' + encodeURIComponent(orderNo), 'GET').then(function (res) {
          if (!res.body || !res.body.data) return;
          showOrder(res.body.data);
          var status = res.body.data.status;
          if (status === 'COMPLETED' || status === 'CANCELLED') stopPay();
        }).catch(function () {});
      }
      pull();
      pollTimer = root.setInterval(pull, 3000);
    }

    function loadPlans() {
      if (!planList || paying) return;
      api('/v1/auth/web/plans', 'GET').then(function (res) {
        if (res.status === 401 || !res.body || !res.body.data) return;
        var items = res.body.data.items || [];
        while (planList.firstChild) planList.removeChild(planList.firstChild);
        if (!items.length) {
          var empty = doc.createElement('p');
          empty.textContent = '现在没有在售套餐。';
          planList.appendChild(empty);
          return;
        }
        items.forEach(function (plan) {
          var card = doc.createElement('article');
          card.className = 'buy-plan';
          var title = doc.createElement('strong');
          title.textContent = planTitle(plan);
          var price = doc.createElement('b');
          price.textContent = moneyText(plan.list_price) + ' USDT';
          var button = doc.createElement('button');
          button.type = 'button';
          button.className = 'btn btn-primary';
          button.textContent = '购买';
          button.addEventListener('click', function () { buy(plan, button); });
          card.appendChild(title);
          card.appendChild(price);
          card.appendChild(button);
          planList.appendChild(card);
        });
      }).catch(function () {
        show(buyError, '套餐没有加载出来，请刷新后再试');
      });
    }

    function buy(plan, button) {
      if (button.disabled) return;
      button.disabled = true;
      show(buyError, '');
      show(payOk, '');
      api('/v1/auth/web/quotes', 'POST', { plan_code: plan.code }).then(function (res) {
        if (!res.body || !res.body.data || !res.body.data.quote_id) {
          throw new Error((res.body && res.body.error && res.body.error.message) || '报价没有生成');
        }
        return api('/v1/auth/web/orders', 'POST', {
          quote_id: res.body.data.quote_id,
          pay_method: 'USDT_TRC20',
          idempotency_key: uuid()
        });
      }).then(function (res) {
        if (!res.body || res.status !== 201 || !res.body.data) {
          throw new Error((res.body && res.body.error && res.body.error.message) || '订单没有创建');
        }
        showOrder(res.body.data);
        watchOrder(res.body.data.order_no);
      }).catch(function (err) {
        show(buyError, (err && err.message) || '购买没有开始，请稍后再试');
        button.disabled = false;
      });
    }

    if (copyAmount) copyAmount.addEventListener('click', function () {
      var text = (payAmount && payAmount.textContent || '').replace(' USDT', '');
      if (root.navigator.clipboard) root.navigator.clipboard.writeText(text);
    });
    if (copyAddress) copyAddress.addEventListener('click', function () {
      if (payAddress && root.navigator.clipboard) root.navigator.clipboard.writeText(payAddress.textContent || '');
    });
    if (cancelOrder) cancelOrder.addEventListener('click', function () {
      var orderNo = cancelOrder.dataset.order || '';
      if (!orderNo) return;
      cancelOrder.disabled = true;
      api('/v1/auth/web/orders/' + encodeURIComponent(orderNo) + '/cancel', 'POST', {}).then(function (res) {
        if (!res.body || !res.body.data) throw new Error((res.body && res.body.error && res.body.error.message) || '取消失败');
        stopPay();
        showOrder(res.body.data);
      }).catch(function (err) {
        show(buyError, (err && err.message) || '取消失败');
      }).then(function () { cancelOrder.disabled = false; });
    });

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
      loadPlans();
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
