// src/lib/node-xhr.ts
// XMLHttpRequest polyfill that uses Node fetch underneath. Installed into
// the jsdom window so that AWSC's um.js can POST to ynuf.aliapp.org.
import { BASE } from './constants';

export interface XhrLogEntry {
  method: string;
  url: string | null;
  headers: Record<string, string>;
  body: string | null;
  status: number | null;
  responseText: string | null;
  error?: string;
}

export interface NodeXHRResult {
  NodeXHR: any;
  xhrLog: XhrLogEntry[];
}

export function createNodeXHR(): NodeXHRResult {
  const xhrLog: XhrLogEntry[] = [];

  function NodeXHR(this: any) {
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

  NodeXHR.prototype.open = function (method: string, url: string) {
    this._method = method;
    this._url = url;
  };
  NodeXHR.prototype.setRequestHeader = function (k: string, v: string) {
    this._headers[k] = v;
  };
  NodeXHR.prototype.addEventListener = function (ev: string, fn: any) {
    (this._listeners[ev] = this._listeners[ev] || []).push(fn);
  };
  NodeXHR.prototype.removeEventListener = function () {};
  NodeXHR.prototype.abort = function () {};

  NodeXHR.prototype.send = function (body?: any) {
    const self = this;
    const rec: XhrLogEntry = {
      method: this._method,
      url: this._url,
      headers: { ...this._headers },
      body: body ? (typeof body === 'string' ? body : String(body)) : null,
      status: null,
      responseText: null,
    };
    xhrLog.push(rec);

    let absUrl: string;
    try {
      absUrl = new URL(this._url, BASE + '/').toString();
    } catch (e: any) {
      rec.error = e.message;
      this._fail(e);
      return;
    }

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
        try { self.onreadystatechange && self.onreadystatechange(); } catch {}
        try { self.onload && self.onload(); } catch {}
        const ls = self._listeners['load'];
        if (ls) ls.forEach((fn: any) => { try { fn(); } catch {} });
      })
      .catch((e: Error) => {
        rec.error = e.message;
        self._fail(e);
      });
  };

  NodeXHR.prototype._fail = function (e: Error) {
    this.readyState = 4;
    this.status = 0;
    try { this.onerror && this.onerror(e); } catch {}
    try { this.onreadystatechange && this.onreadystatechange(); } catch {}
    const ls = this._listeners['error'];
    if (ls) ls.forEach((fn: any) => { try { fn(e); } catch {} });
  };

  return { NodeXHR, xhrLog };
}
