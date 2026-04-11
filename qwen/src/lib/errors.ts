// src/lib/errors.ts
// Typed error hierarchy. Every helper throws one of these on failure —
// never a plain Error, and never as a "error chunk" inside a stream.
//
//   QwenError                  (base)
//   ├── QwenRateLimitedError   code='RateLimited'   .retryAfterHours
//   ├── QwenUnauthorizedError  code='Unauthorized'
//   ├── QwenBadRequestError    code='Bad_Request'
//   ├── QwenServerError        code='Internal_Server_Error'
//   └── QwenNetworkError       code='NetworkError'   .cause
//
// Callers distinguish errors with `instanceof` OR `.code` — whichever
// is more ergonomic in their codebase.

export interface QwenErrorOptions {
  code?: string;
  status?: number | null;
  retryAfterHours?: number | null;
  response?: any;
}

export class QwenError extends Error {
  public code: string | null;
  public status: number | null;
  public retryAfterHours: number | null;
  public response: any;

  constructor(message: string, opts: QwenErrorOptions = {}) {
    super(message);
    this.name = 'QwenError';
    this.code = opts.code ?? null;
    this.status = opts.status ?? null;
    this.retryAfterHours = opts.retryAfterHours ?? null;
    this.response = opts.response ?? null;
  }
}

export class QwenRateLimitedError extends QwenError {
  constructor(message: string, opts: QwenErrorOptions = {}) {
    super(message, opts);
    this.name = 'QwenRateLimitedError';
    this.code = 'RateLimited';
  }
}

export class QwenUnauthorizedError extends QwenError {
  constructor(message: string, opts: QwenErrorOptions = {}) {
    super(message, opts);
    this.name = 'QwenUnauthorizedError';
    this.code = 'Unauthorized';
  }
}

export class QwenBadRequestError extends QwenError {
  constructor(message: string, opts: QwenErrorOptions = {}) {
    super(message, opts);
    this.name = 'QwenBadRequestError';
    this.code = 'Bad_Request';
  }
}

export class QwenServerError extends QwenError {
  constructor(message: string, opts: QwenErrorOptions = {}) {
    super(message, opts);
    this.name = 'QwenServerError';
    this.code = 'Internal_Server_Error';
  }
}

export class QwenNetworkError extends QwenError {
  public cause?: Error;

  constructor(message: string, opts: QwenErrorOptions = {}) {
    super(message, opts);
    this.name = 'QwenNetworkError';
    this.code = 'NetworkError';
  }
}

/**
 * Parses one of Qwen's `{success:false, data:{code,details,num?}}` error
 * envelopes (which arrives as HTTP 200 + application/json) and returns
 * the appropriate subclass.
 */
export function errorFromEnvelope(parsed: any, rawText?: string): QwenError {
  const data = (parsed && parsed.data) || {};
  const code: string = data.code || 'unknown';
  const details: string =
    data.details || data.template || (rawText || '').slice(0, 300);
  const opts: QwenErrorOptions = {
    code,
    retryAfterHours: data.num ?? null,
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

/** Wraps a native fetch/TLS failure in a `QwenNetworkError`. */
export function wrapNetworkError(
  originalError: Error,
  context = ''
): QwenNetworkError {
  const msg = context
    ? `${context}: ${originalError.message}`
    : originalError.message;
  const err = new QwenNetworkError(msg, { response: originalError });
  err.cause = originalError;
  return err;
}
