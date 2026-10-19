// 宇海官网注册。自包含：SHA-256 工作量证明、表单校验、调用 api.yhvpn.io。
// 不加载任何第三方脚本。算法必须与服务端 sha256-lzbits-v1 一致：
// preimage = challenge_id + ":" + nonce + ":" + 十进制 counter，SHA-256 前导零达到 difficulty。
(function (root) {
  'use strict';

  function rotr(x, n) { return (x >>> n) | (x << (32 - n)); }

  function sha256(bytes) {
    var K = new Uint32Array([
      0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
      0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
      0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
      0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
      0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
      0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
      0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
      0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2
    ]);
    var H = new Uint32Array([
      0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19
    ]);
    var bitLen = bytes.length * 8;
    var withPad = new Uint8Array(((bytes.length + 9 + 63) >> 6) << 6);
    withPad.set(bytes);
    withPad[bytes.length] = 0x80;
    var view = new DataView(withPad.buffer);
    view.setUint32(withPad.length - 4, bitLen >>> 0, false);
    var w = new Uint32Array(64);
    var off, i, s0, s1, a, b, c, d, e, f, g, h, S0, S1, ch, maj, t1, t2;
    for (off = 0; off < withPad.length; off += 64) {
      for (i = 0; i < 16; i++) w[i] = view.getUint32(off + i * 4, false);
      for (i = 16; i < 64; i++) {
        s0 = rotr(w[i - 15], 7) ^ rotr(w[i - 15], 18) ^ (w[i - 15] >>> 3);
        s1 = rotr(w[i - 2], 17) ^ rotr(w[i - 2], 19) ^ (w[i - 2] >>> 10);
        w[i] = (w[i - 16] + s0 + w[i - 7] + s1) >>> 0;
      }
      a = H[0]; b = H[1]; c = H[2]; d = H[3]; e = H[4]; f = H[5]; g = H[6]; h = H[7];
      for (i = 0; i < 64; i++) {
        S1 = rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25);
        ch = (e & f) ^ (~e & g);
        t1 = (h + S1 + ch + K[i] + w[i]) >>> 0;
        S0 = rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22);
        maj = (a & b) ^ (a & c) ^ (b & c);
        t2 = (S0 + maj) >>> 0;
        h = g; g = f; f = e; e = (d + t1) >>> 0; d = c; c = b; b = a; a = (t1 + t2) >>> 0;
      }
      H[0] = (H[0] + a) >>> 0; H[1] = (H[1] + b) >>> 0; H[2] = (H[2] + c) >>> 0; H[3] = (H[3] + d) >>> 0;
      H[4] = (H[4] + e) >>> 0; H[5] = (H[5] + f) >>> 0; H[6] = (H[6] + g) >>> 0; H[7] = (H[7] + h) >>> 0;
    }
    var out = new Uint8Array(32);
    var ov = new DataView(out.buffer);
    for (i = 0; i < 8; i++) ov.setUint32(i * 4, H[i], false);
    return out;
  }

  function sha256Hex(text) {
    var digest = sha256(new TextEncoder().encode(text));
    var hex = '';
    for (var i = 0; i < digest.length; i++) hex += (digest[i] < 16 ? '0' : '') + digest[i].toString(16);
    return hex;
  }

  function leadingZeroBits(digest) {
    var n = 0;
    for (var i = 0; i < digest.length; i++) {
      var b = digest[i];
      if (b === 0) { n += 8; continue; }
      for (var mask = 128; mask > 0; mask >>= 1) {
        if (b & mask) return n;
        n++;
      }
    }
    return n;
  }

  function solve(id, nonce, bits, onProgress) {
    var prefix = new TextEncoder().encode(id + ':' + nonce + ':');
    var buf = new Uint8Array(prefix.length + 16);
    buf.set(prefix);
    var counter = 0;
    return new Promise(function (resolve, reject) {
      function step() {
        var end = counter + 6000;
        for (; counter < end; counter++) {
          if (counter > 16777215) { reject(new Error('验证失败')); return; }
          var s = String(counter);
          for (var i = 0; i < s.length; i++) buf[prefix.length + i] = s.charCodeAt(i);
          if (leadingZeroBits(sha256(buf.subarray(0, prefix.length + s.length))) >= bits) {
            resolve(counter);
            return;
          }
        }
        if (onProgress) onProgress(counter);
        setTimeout(step, 0);
      }
      step();
    });
  }

  function looksLikeAddress(s) {
    if (s.length !== 34 || s.charAt(0) !== 'T') return false;
    var alphabet = '123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz';
    for (var i = 0; i < s.length; i++) if (alphabet.indexOf(s.charAt(i)) < 0) return false;
    return true;
  }

  function validPassword(p) {
    if (Array.from(p).length < 8 || new TextEncoder().encode(p).length > 128) return false;
    var letter = false, digit = false;
    for (var i = 0; i < p.length; i++) {
      var c = p.charCodeAt(i);
      if (c >= 48 && c <= 57) digit = true;
      else if ((c >= 65 && c <= 90) || (c >= 97 && c <= 122)) letter = true;
    }
    return letter && digit;
  }

  function inviteCodeFrom(search, pathname) {
    var params = new URLSearchParams(search || '');
    var q = params.get('code') || params.get('c') || '';
    if (!q) {
      var parts = String(pathname || '').split('/').filter(Boolean);
      var at = parts.indexOf('i');
      if (at >= 0 && parts[at + 1]) q = parts[at + 1];
    }
    q = String(q).trim().toUpperCase();
    if (!/^[23456789ABCDEFGHJKMNPQRSTUVWXYZ]{4,12}$/.test(q)) return '';
    return q;
  }

  var API_ORIGIN = 'https://api.yhvpn.io';

  function apiBase(doc) {
    var meta = doc.querySelector('meta[name="yuhai-api"]');
    var declared = meta && meta.content ? String(meta.content).trim().replace(/\/$/, '') : API_ORIGIN;
    if (declared !== API_ORIGIN) return '';
    return API_ORIGIN;
  }

  function showError(el, text) {
    if (!el) return;
    el.hidden = !text;
    el.textContent = text || '';
  }

  function boot() {
    var doc = root.document;
    var form = doc.getElementById('reg-form');
    if (!form) return;
    var addressEl = doc.getElementById('address');
    var passwordEl = doc.getElementById('password');
    var againEl = doc.getElementById('password2');
    var inviteEl = doc.getElementById('invite');
    var hpEl = doc.getElementById('yh_hp');
    var powEl = doc.getElementById('pow');
    var errorEl = doc.getElementById('form-error');
    var submitEl = doc.getElementById('submit');
    var panel = doc.getElementById('reg-panel');
    var done = doc.getElementById('reg-done');
    var doneAddress = doc.getElementById('done-address');
    var code = inviteCodeFrom(root.location.search, root.location.pathname);
    if (code && inviteEl && !inviteEl.value) inviteEl.value = code;

    var copyBtn = doc.getElementById('copy-address');
    if (copyBtn && doneAddress) {
      copyBtn.addEventListener('click', function () {
        var text = doneAddress.textContent.trim();
        var mark = function () {
          copyBtn.textContent = '已复制';
          setTimeout(function () { copyBtn.textContent = '复制'; }, 1600);
        };
        if (root.navigator.clipboard && root.navigator.clipboard.writeText) {
          root.navigator.clipboard.writeText(text).then(mark, function () {});
        }
      });
    }

    form.addEventListener('submit', function (event) {
      event.preventDefault();
      if (submitEl && submitEl.disabled) return;
      var address = addressEl.value.trim();
      var password = passwordEl.value;
      var again = againEl.value;
      var invite = inviteEl.value.trim().toUpperCase();
      showError(errorEl, '');
      if (!looksLikeAddress(address)) {
        showError(errorEl, '请填写正确的 USDT（TRC20）地址，以 T 开头，共 34 位');
        addressEl.focus();
        return;
      }
      if (!validPassword(password)) {
        showError(errorEl, '密码至少 8 位，且同时包含字母和数字');
        passwordEl.focus();
        return;
      }
      if (password !== again) {
        showError(errorEl, '两次输入的密码不一致');
        againEl.focus();
        return;
      }
      var base = apiBase(doc);
      if (base !== API_ORIGIN) {
        showError(errorEl, '注册接口不可用，请刷新页面后再试');
        return;
      }
      submitEl.disabled = true;
      if (powEl) powEl.textContent = '正在获取人机验证…';
      fetch(base + '/v1/auth/web/challenge', {
        method: 'POST',
        mode: 'cors',
        credentials: 'omit',
        headers: { 'Content-Type': 'application/json' },
        body: '{}'
      }).then(function (res) {
        return res.json().then(function (body) { return { status: res.status, body: body }; });
      }).then(function (res) {
        if (res.status !== 200 || !res.body || !res.body.data) {
          var msg = (res.body && res.body.error && res.body.error.message) || '人机验证暂时不可用，请稍后再试';
          throw new Error(msg);
        }
        var ch = res.body.data;
        if (ch.algorithm !== 'sha256-lzbits-v1') throw new Error('人机验证暂时不可用，请稍后再试');
        if (powEl) powEl.textContent = '正在完成人机验证，请稍候…';
        return solve(ch.challenge_id, ch.nonce, ch.difficulty, function () {
          if (powEl) powEl.textContent = '正在完成人机验证，请稍候…';
        }).then(function (counter) {
          return fetch(base + '/v1/auth/web/register', {
            method: 'POST',
            mode: 'cors',
            credentials: 'omit',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
              address: address,
              password: password,
              invite_code: invite,
              yh_hp: hpEl ? hpEl.value : '',
              challenge_id: ch.challenge_id,
              counter: counter
            })
          }).then(function (r) {
            return r.json().then(function (body) { return { status: r.status, body: body }; }, function () { return { status: r.status, body: null }; });
          });
        });
      }).then(function (res) {
        if (!res || res.status !== 201 || !res.body || !res.body.data) {
          var msg = (res && res.body && res.body.error && res.body.error.message) || '注册没有完成，请稍后再试';
          throw new Error(msg);
        }
        if (doneAddress) doneAddress.textContent = res.body.data.login_address || address;
        if (panel) panel.hidden = true;
        if (done) {
          done.hidden = false;
          done.scrollIntoView({ block: 'start' });
        }
      }).catch(function (err) {
        showError(errorEl, (err && err.message) || '网络暂时连不上，请稍后再试');
        if (powEl) powEl.textContent = '提交时自动完成，大约需要一两秒，不使用第三方验证码。';
        submitEl.disabled = false;
      });
    });
  }

  if (root.document && root.document.getElementById && root.document.getElementById('reg-form')) boot();

  var api = {
    sha256Hex: sha256Hex,
    leadingZeroBits: leadingZeroBits,
    inviteCodeFrom: inviteCodeFrom,
    looksLikeAddress: looksLikeAddress,
    validPassword: validPassword,
    solve: solve
  };
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof globalThis !== 'undefined' ? globalThis : this);
