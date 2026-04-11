// src/lib/errors.js
// Typed error hierarchy. Every helper and stream throws one of these on
// failure — never an unwrapped Error, never an "error chunk" in a stream.
//
// Layout:
//
//   QwenError                  (base class — code, status, retryAfterHours, response)
//   ├── QwenRateLimitedError   (code = 'RateLimited')
//   ├── QwenUnauthorizedError  (code = 'Unauthorized')
//   ├── QwenBadRequestError    (code = 'Bad_Request')
//   ├── QwenServerError        (code = 'Internal_Server_Error')
//   └── QwenNetworkError       (code = 'NetworkError' — fetch/TLS/DNS failure)
//
// Callers can distinguish errors two ways:
//   if (e instanceof QwenRateLimitedError) { ... }   // preferred
//   if (e.code === 'RateLimited')          { ... }   // also works
'use strict';

class QwenError extends Error {
  constructor(message, opts = {}) {
    super(message);
    this.name = 'QwenError';
    this.code = opts.code || null;
    this.status = opts.status || null;
    this.retryAfterHours = opts.retryAfterHours || null;
    this.response = opts.response || null;
  }
}

class QwenRateLimitedError extends QwenError {
  constructor(message, opts = {}) {
    super(message, opts);
    this.name = 'QwenRateLimitedError';
    this.code = 'RateLimited';
  }
}

class QwenUnauthorizedError extends QwenError {
  constructor(message, opts = {}) {
    super(message, opts);
    this.name = 'QwenUnauthorizedError';
    this.code = 'Unauthorized';
  }
}

class QwenBadRequestError extends QwenError {
  constructor(message, opts = {}) {
    super(message, opts);
    this.name = 'QwenBadRequestError';
    this.code = 'Bad_Request';
  }
}

class QwenServerError extends QwenError {
  constructor(message, opts = {}) {
    super(message, opts);
    this.name = 'QwenServerError';
    this.code = 'Internal_Server_Error';
  }
}

class QwenNetworkError extends QwenError {
  constructor(message, opts = {}) {
    super(message, opts);
    this.name = 'QwenNetworkError';
    this.code = 'NetworkError';
  }
}

// Factory: takes an HTTP 200 + application/json error envelope from Qwen
// and returns the appropriate subclass. The envelope shape is:
//
//   { success: false, request_id: "...", data: { code, details, template?, num? } }
//
// `num` is the "retry after N hours" hint that comes with RateLimited.
function errorFromEnvelope(parsed, rawText) {
  const data = (parsed && parsed.data) || {};
  const code = data.code || 'unknown';
  const details = data.details || data.template || (rawText || '').slice(0, 300);
  const opts = {
    code,
    retryAfterHours: data.num || null,
    response: parsed || rawText || null,
  };
  switch (code) {
    case 'RateLimited':
      return new QwenRateLimitedError(details, opts);
    case 'Unauthorized':
      return new QwenUnauthorizedError(details, opts);
    case 'Bad_Request':
      return new QwenBadRequestError(details, opts);
    case 'Internal_Server_Error':
      return new QwenServerError(details, opts);
    default:
      return new QwenError(`unknown error code "${code}": ${details}`, opts);
  }
}

// Wrap a native fetch/TLS failure in a QwenNetworkError.
function wrapNetworkError(originalError, context = '') {
  const msg = context
    ? `${context}: ${originalError.message}`
    : originalError.message;
  const err = new QwenNetworkError(msg, { response: originalError });
  err.cause = originalError;
  return err;
}

module.exports = {
  QwenError,
  QwenRateLimitedError,
  QwenUnauthorizedError,
  QwenBadRequestError,
  QwenServerError,
  QwenNetworkError,
  errorFromEnvelope,
  wrapNetworkError,
};
