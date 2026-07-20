/** @typedef {import('@anthropic-ai/claude-agent-sdk').PermissionResult} PermissionResult */

export const MSG_RUN_REQUEST = 'run_request';
export const MSG_PERMISSION_REQUEST = 'permission_request';
export const MSG_PERMISSION_RESPONSE = 'permission_response';

/** Keys stripped so bare allowedTools cannot auto-approve past canUseTool. */
const STRIPPED_QUERY_OPTION_KEYS = new Set(['allowedTools']);

/** Bridge always prompts; these modes auto-approve or bypass canUseTool. */
const FORBIDDEN_PERMISSION_MODES = new Set(['bypassPermissions', 'acceptEdits', 'auto']);

export const A2GENT_PRE_TOOL_USE_REASON = 'A2gent requires per-tool approval';

/**
 * WHY: canUseTool alone is not enough — run_request hooks or SDK defaults can
 * auto-allow tools before the bridge sees them. PreToolUse permissionDecision
 * 'ask' forces every tool through the permission path; canUseTool then blocks
 * on A2gent's permission_response. Run requests cannot strip this matcher.
 *
 * @returns {import('@anthropic-ai/claude-agent-sdk').HookJSONOutput}
 */
export async function a2gentPreToolUseHook() {
  return {
    hookSpecificOutput: {
      hookEventName: 'PreToolUse',
      permissionDecision: 'ask',
      permissionDecisionReason: A2GENT_PRE_TOOL_USE_REASON,
    },
  };
}

/** Catches every tool (matcher omitted); always prepended to PreToolUse hooks. */
export const A2GENT_PRE_TOOL_USE_MATCHER = {
  hooks: [a2gentPreToolUseHook],
};

/**
 * @param {unknown} requestHooks
 * @returns {Record<string, import('@anthropic-ai/claude-agent-sdk').HookCallbackMatcher[]>}
 */
export function mergeMandatoryHooks(requestHooks) {
  const hooks =
    requestHooks && typeof requestHooks === 'object' && !Array.isArray(requestHooks)
      ? { ...requestHooks }
      : {};
  const preToolUse = Array.isArray(hooks.PreToolUse) ? hooks.PreToolUse : [];
  hooks.PreToolUse = [A2GENT_PRE_TOOL_USE_MATCHER, ...preToolUse];
  return hooks;
}

/**
 * @param {string} line
 * @returns {Record<string, unknown>}
 */
export function parseNDJSONLine(line) {
  const trimmed = line.trim();
  if (!trimmed) {
    throw new Error('empty NDJSON line');
  }
  const value = JSON.parse(trimmed);
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error('NDJSON line must be a JSON object');
  }
  return value;
}

/**
 * @param {unknown} message
 * @returns {message is Record<string, unknown> & { type: string }}
 */
export function isTypedMessage(message) {
  return Boolean(message && typeof message === 'object' && !Array.isArray(message) && typeof message.type === 'string');
}

/**
 * @param {Record<string, unknown>} message
 * @returns {boolean}
 */
export function isRunRequest(message) {
  return message.type === MSG_RUN_REQUEST;
}

/**
 * @param {Record<string, unknown>} message
 * @returns {boolean}
 */
export function isPermissionResponse(message) {
  return message.type === MSG_PERMISSION_RESPONSE;
}

/**
 * Remove auto-approve knobs; keep explicit permission prompts.
 *
 * @param {Record<string, unknown> | undefined | null} options
 * @returns {Record<string, unknown>}
 */
export function sanitizeQueryOptions(options) {
  const out = { ...(options ?? {}) };
  for (const key of STRIPPED_QUERY_OPTION_KEYS) {
    delete out[key];
  }
  if (
    out.permissionMode == null ||
    out.permissionMode === '' ||
    FORBIDDEN_PERMISSION_MODES.has(String(out.permissionMode))
  ) {
    out.permissionMode = 'default';
  }
  return out;
}

/**
 * @param {string} toolName
 * @param {Record<string, unknown>} input
 * @param {Record<string, unknown>} context
 * @returns {Record<string, unknown>}
 */
export function buildPermissionRequest(toolName, input, context) {
  const request = {
    type: MSG_PERMISSION_REQUEST,
    requestId: String(context.requestId ?? ''),
    toolUseID: String(context.toolUseID ?? ''),
    toolName,
    input,
  };
  for (const key of [
    'title',
    'displayName',
    'description',
    'blockedPath',
    'decisionReason',
    'agentID',
    'suggestions',
    'matchedAskRule',
  ]) {
    if (context[key] !== undefined) {
      request[key] = context[key];
    }
  }
  return request;
}

/**
 * Merge AskUserQuestion answers into updatedInput when provided separately.
 *
 * @param {Record<string, unknown>} originalInput
 * @param {Record<string, unknown>} response
 * @returns {Record<string, unknown>}
 */
export function buildAskUserUpdatedInput(originalInput, response) {
  const updatedInput = {
    ...originalInput,
    ...(response.updatedInput && typeof response.updatedInput === 'object' && !Array.isArray(response.updatedInput)
      ? response.updatedInput
      : {}),
  };
  if (response.answers && typeof response.answers === 'object' && !Array.isArray(response.answers)) {
    updatedInput.answers = response.answers;
  }
  if (originalInput.questions !== undefined) {
    updatedInput.questions = originalInput.questions;
  }
  return updatedInput;
}

/**
 * @param {Record<string, unknown>} response
 * @param {Record<string, unknown>} [originalInput]
 * @param {string} [toolName]
 * @returns {PermissionResult}
 */
export function permissionResponseToResult(response, originalInput = {}, toolName = '') {
  const requestId = String(response.requestId ?? '');
  const toolUseID = response.toolUseID != null ? String(response.toolUseID) : undefined;
  const behavior = String(response.behavior ?? '');

  if (behavior === 'allow') {
    let updatedInput =
      response.updatedInput && typeof response.updatedInput === 'object' && !Array.isArray(response.updatedInput)
        ? { ...response.updatedInput }
        : { ...originalInput };

    if (toolName === 'AskUserQuestion' || response.answers != null) {
      updatedInput = buildAskUserUpdatedInput(originalInput, response);
    }

    /** @type {PermissionResult} */
    const result = {
      behavior: 'allow',
      updatedInput,
    };
    if (toolUseID) {
      result.toolUseID = toolUseID;
    }
    if (Array.isArray(response.updatedPermissions)) {
      result.updatedPermissions = response.updatedPermissions;
    }
    return result;
  }

  if (behavior === 'deny') {
    /** @type {PermissionResult} */
    const result = {
      behavior: 'deny',
      message: typeof response.message === 'string' && response.message.trim() ? response.message : 'denied',
    };
    if (toolUseID) {
      result.toolUseID = toolUseID;
    }
    if (response.interrupt === true) {
      result.interrupt = true;
    }
    return result;
  }

  throw new Error(`invalid permission_response behavior for requestId=${requestId || '(missing)'}`);
}

/**
 * @param {unknown} error
 * @returns {boolean}
 */
export function isAbortError(error) {
  if (!error || typeof error !== 'object') {
    return false;
  }
  return error.name === 'AbortError' || error.code === 'ABORT_ERR';
}

export class PermissionBroker {
  constructor() {
    /** @type {Map<string, { resolve: (value: Record<string, unknown>) => void, reject: (reason?: unknown) => void, onAbort: () => void, signal: AbortSignal }>} */
    this.pending = new Map();
  }

  /**
   * @param {string} requestId
   * @param {AbortSignal} signal
   * @returns {Promise<Record<string, unknown>>}
   */
  waitForResponse(requestId, signal) {
    if (!requestId) {
      return Promise.reject(new Error('permission requestId required'));
    }
    if (this.pending.has(requestId)) {
      return Promise.reject(new Error(`duplicate permission requestId: ${requestId}`));
    }

    return new Promise((resolve, reject) => {
      const onAbort = () => {
        this.pending.delete(requestId);
        const err = new Error('permission request aborted');
        err.name = 'AbortError';
        err.code = 'ABORT_ERR';
        reject(err);
      };

      if (signal.aborted) {
        onAbort();
        return;
      }

      signal.addEventListener('abort', onAbort, { once: true });
      this.pending.set(requestId, { resolve, reject, onAbort, signal });
    });
  }

  /**
   * @param {Record<string, unknown>} message
   * @returns {boolean}
   */
  handleResponse(message) {
    if (!isPermissionResponse(message)) {
      return false;
    }
    const requestId = String(message.requestId ?? '');
    const entry = this.pending.get(requestId);
    if (!entry) {
      return false;
    }
    entry.signal.removeEventListener('abort', entry.onAbort);
    this.pending.delete(requestId);
    entry.resolve(message);
    return true;
  }

  /**
   * @param {string} [reason]
   */
  abortAll(reason = 'sidecar shutting down') {
    for (const [requestId, entry] of this.pending) {
      entry.signal.removeEventListener('abort', entry.onAbort);
      const err = new Error(reason);
      err.name = 'AbortError';
      err.code = 'ABORT_ERR';
      entry.reject(err);
      this.pending.delete(requestId);
    }
  }

  get size() {
    return this.pending.size;
  }
}

/**
 * @param {Record<string, unknown>} runRequest
 * @returns {{ prompt: string | AsyncIterable<Record<string, unknown>>, options: Record<string, unknown> }}
 */
export function parseRunRequest(runRequest) {
  if (!isRunRequest(runRequest)) {
    throw new Error(`expected ${MSG_RUN_REQUEST}, got ${String(runRequest.type)}`);
  }
  const prompt = runRequest.prompt;
  if (typeof prompt !== 'string' && !(prompt && typeof prompt[Symbol.asyncIterator] === 'function')) {
    throw new Error('run_request.prompt must be a string');
  }
  const options = sanitizeQueryOptions(
    runRequest.options && typeof runRequest.options === 'object' && !Array.isArray(runRequest.options)
      ? runRequest.options
      : {},
  );
  return { prompt, options };
}
