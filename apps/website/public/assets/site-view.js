(function () {
  'use strict';
  var path = '/';
  try {
    path = (location.pathname || '/').slice(0, 128);
  } catch (e) {
    path = '/';
  }
  if (path.charAt(0) !== '/') path = '/';
  fetch('https://api.yhvpn.io/v1/site/pageview', {
    method: 'POST',
    mode: 'cors',
    credentials: 'omit',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path: path }),
    keepalive: true
  }).catch(function () {});
})();
