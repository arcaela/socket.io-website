// src/lib/node-xhr.js
// An XMLHttpRequest polyfill that uses Node's built-in fetch underneath.
// Installed into the jsdom window so that AWSC's um.js can call
// `new XMLHttpRequest()` and POST to ynuf.aliapp.org — jsdom's own XHR
// implementation would try to use its internal resource loader, which we
// can't easily wire through our proxy.
//
// As a side benefit, every call gets recorded into `xhrLog` so that
// tokens.js can poll for the umid registration response.
'use strict';

const { BASE } = require('./constants');

function createNodeXHR() {
  const xhrLog = [];

  function NodeXHR() {
    this._headers = {};
    this._url = null;
    this._method = 'GET';
    this._listeners = {};
    this.readyState = 0;
    this.status = 0;
    this.responseText = '';
    this.response = '';
    this.withCredentials = false;
  }

  NodeXHR.prototype.open = function (method, url) {
    this._method = method;
    this._url = url;
  };
  NodeXHR.prototype.setRequestHeader = function (k, v) {
    this._headers[k] = v;
  };
  NodeXHR.prototype.addEventListener = function (ev, fn) {
    (this._listeners[ev] = this._listeners[ev] || []).push(fn);
  };
  NodeXHR.prototype.removeEventListener = function () {};
  NodeXHR.prototype.abort = function () {};

  NodeXHR.prototype.send = function (body) {
    const self = this;
    const rec = {
      method: this._method,
      url: this._url,
      headers: { ...this._headers },
      body: body ? (typeof body === 'string' ? body : String(body)) : null,
      status: null,
      responseText: null,
    };
    xhrLog.push(rec);

    let absUrl;
    try { absUrl = new URL(this._url, BASE + '/').toString(); }
    catch (e) { rec.error = e.message; this._fail(e); return; }

    fetch(absUrl, {
      method: this._method,
      headers: this._headers,
      body: body || undefined,
    })
      .then(async (res) => {
        self.readyState = 4;
        self.status = res.status;
        const text = await res.text();
        self.responseText = text;
        self.response = text;
        rec.status = res.status;
        rec.responseText = text;
        try { self.onreadystatechange && self.onreadystatechange(); } catch (_) {}
        try { self.onload && self.onload(); } catch (_) {}
        const ls = self._listeners['load'];
        if (ls) ls.forEach((fn) => { try { fn(); } catch (_) {} });
      })
      .catch((e) => {
        rec.error = e.message;
        self._fail(e);
      });
  };

  NodeXHR.prototype._fail = function (e) {
    this.readyState = 4;
    this.status = 0;
    try { this.onerror && this.onerror(e); } catch (_) {}
    try { this.onreadystatechange && this.onreadystatechange(); } catch (_) {}
    const ls = this._listeners['error'];
    if (ls) ls.forEach((fn) => { try { fn(e); } catch (_) {} });
  };

  return { NodeXHR, xhrLog };
}

module.exports = { createNodeXHR };
