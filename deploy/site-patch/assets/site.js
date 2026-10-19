// 宇海官网脚本：首屏连接动效示意、三幕图库、进入视区淡入、复制校验值、邀请码。无第三方依赖。
(function () {
  'use strict';
  // 首页主按钮按系统跳到对应安装包；其他系统仍进下载页，不猜一个不存在的客户端。
  var ua = navigator.userAgent || '';
  var android = /Android/i.test(ua);
  var windows = /Windows NT/i.test(ua);
  var direct = android
    ? 'https://api.yhvpn.io/v1/site/downloads/android'
    : windows
      ? 'https://api.yhvpn.io/v1/site/downloads/windows'
      : '';
  if (direct) {
    var primary = document.getElementById('primary-download');
    var navDl = document.getElementById('nav-download');
    if (primary) {
      primary.href = direct;
      primary.textContent = android ? '下载安卓版' : '下载 Windows 版';
    }
    if (navDl) navDl.href = direct;
  }
  var mq = window.matchMedia ? window.matchMedia('(prefers-reduced-motion: reduce)') : null;
  var reduce = !!(mq && mq.matches);

  // ---------- 首屏：连接动效示意（未连接 → 连接中 → 已连接 循环；可暂停；减少动效时停在已连接静态帧） ----------
  var phone = document.querySelector('[data-demo-phone]');
  if (phone) {
    var label = phone.querySelector('[data-demo-label]');
    var timer = phone.querySelector('[data-demo-timer]');
    var hint = phone.querySelector('[data-demo-hint]');
    var rates = phone.querySelector('[data-demo-rates]');
    var toggle = document.querySelector('[data-demo-toggle]');
    var step = 0, handle = null, clock = null, started = 0, playing = !reduce;
    var pad = function (n) { return n < 10 ? '0' + n : '' + n; };
    function show(state) {
      phone.setAttribute('data-state', state);
      label.textContent = state === 'connected' ? '已连接' : state === 'connecting' ? '正在连接' : '未连接';
      hint.textContent = state === 'connected' ? '轻触断开' : '轻触连接';
      if (clock) { clearInterval(clock); clock = null; }
      if (state === 'connected') {
        started = Date.now() - 48000;
        var tick = function () {
          var s = Math.floor((Date.now() - started) / 1000);
          timer.textContent = pad(Math.floor(s / 3600)) + ':' + pad(Math.floor(s / 60) % 60) + ':' + pad(s % 60);
        };
        tick();
        if (playing) clock = setInterval(tick, 1000);
        rates.innerHTML = '<span>↓ 2.4 MB/s</span><span>↑ 310 KB/s</span>';
      } else { timer.textContent = ''; rates.innerHTML = ''; }
    }
    var seq = [['idle', 1600], ['connecting', 1700], ['connected', 6000]];
    function next() {
      var s = seq[step % seq.length]; step++;
      show(s[0]);
      handle = setTimeout(next, s[1]);
    }
    function play() { playing = true; toggle.textContent = '暂停动效'; toggle.setAttribute('aria-pressed', 'true'); next(); }
    function pause() {
      playing = false; clearTimeout(handle); handle = null;
      toggle.textContent = '播放动效'; toggle.setAttribute('aria-pressed', 'false');
      show('connected');
    }
    if (toggle) toggle.addEventListener('click', function () { if (playing) pause(); else play(); });
    if (playing) {
      // 只在首屏可见时播放，离开视区暂停计时器
      if ('IntersectionObserver' in window) {
        new IntersectionObserver(function (es) {
          if (!playing) return;
          if (es[0].isIntersecting && !handle) next();
          if (!es[0].isIntersecting && handle) { clearTimeout(handle); handle = null; }
        }).observe(phone);
      } else { next(); }
    } else { pause(); }
  }

  // ---------- 三幕图库：滑动 / 按钮 / 方向键 ----------
  document.querySelectorAll('[data-gallery]').forEach(function (g) {
    var track = g.querySelector('[data-gallery-track]');
    var scenes = track.children;
    var tabs = g.querySelectorAll('[data-gallery-go]');
    var current = 0;
    function go(i) {
      current = Math.max(0, Math.min(scenes.length - 1, i));
      track.scrollTo({ left: scenes[current].offsetLeft - track.offsetLeft, behavior: reduce ? 'auto' : 'smooth' });
      mark();
    }
    function mark() {
      tabs.forEach(function (t, i) {
        if (i === current) t.setAttribute('aria-current', 'true'); else t.removeAttribute('aria-current');
        t.setAttribute('aria-selected', i === current ? 'true' : 'false');
      });
    }
    tabs.forEach(function (t) { t.addEventListener('click', function () { go(+t.getAttribute('data-gallery-go')); }); });
    var prev = g.querySelector('[data-gallery-prev]'), nxt = g.querySelector('[data-gallery-next]');
    if (prev) prev.addEventListener('click', function () { go(current - 1); });
    if (nxt) nxt.addEventListener('click', function () { go(current + 1); });
    track.addEventListener('keydown', function (e) {
      if (e.key === 'ArrowRight') { e.preventDefault(); go(current + 1); }
      if (e.key === 'ArrowLeft') { e.preventDefault(); go(current - 1); }
    });
    var t = null;
    track.addEventListener('scroll', function () {
      clearTimeout(t);
      t = setTimeout(function () {
        var best = 0, dist = Infinity;
        for (var i = 0; i < scenes.length; i++) {
          var d = Math.abs(scenes[i].offsetLeft - track.offsetLeft - track.scrollLeft);
          if (d < dist) { dist = d; best = i; }
        }
        current = best; mark();
      }, 80);
    }, { passive: true });
    mark();
  });

  // ---------- 进入视区淡入（不支持或要求减少动效时直接显示） ----------
  var reveals = document.querySelectorAll('.reveal');
  if (!reduce && 'IntersectionObserver' in window) {
    var io = new IntersectionObserver(function (es) {
      es.forEach(function (e) { if (e.isIntersecting) { e.target.classList.add('in'); io.unobserve(e.target); } });
    }, { rootMargin: '0px 0px -8% 0px' });
    reveals.forEach(function (el) { io.observe(el); });
  } else {
    reveals.forEach(function (el) { el.classList.add('in'); });
  }

  // ---------- 复制校验值 ----------
  function copyText(btn, text, el, after) {
    var done = function () {
      btn.setAttribute('data-done', '');
      var old = btn.textContent;
      btn.textContent = '已复制';
      if (after) after();
      setTimeout(function () { btn.removeAttribute('data-done'); btn.textContent = old; }, 1600);
    };
    var fallback = function () {
      if (!el) return;
      var range = document.createRange(); range.selectNodeContents(el);
      var sel = window.getSelection(); sel.removeAllRanges(); sel.addRange(range);
    };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(done, fallback);
    } else { fallback(); }
  }
  document.querySelectorAll('[data-copy]').forEach(function (btn) {
    btn.addEventListener('click', function () {
      var el = document.getElementById(btn.getAttribute('data-copy'));
      if (el) copyText(btn, el.textContent.trim(), el);
    });
  });

  // ---------- 邀请页：从路径读取邀请码；只有用户点按钮才复制 ----------
  var codeEl = document.getElementById('invite-code');
  if (codeEl) {
    var code = (location.pathname.split('/').filter(Boolean)[1] || '').toUpperCase();
    if (/^[23456789ABCDEFGHJKMNPQRSTUVWXYZ]{6}$/.test(code)) {
      codeEl.textContent = code;
      var btn = document.getElementById('invite-copy');
      var note = document.getElementById('invite-note');
      if (btn) {
        btn.hidden = false;
        btn.addEventListener('click', function () {
          copyText(btn, code, codeEl, function () { if (note) note.hidden = false; });
        });
      }
      var registerLink = document.getElementById('invite-register');
      if (registerLink) registerLink.href = '/register/?code=' + encodeURIComponent(code);
    }
  }
})();
