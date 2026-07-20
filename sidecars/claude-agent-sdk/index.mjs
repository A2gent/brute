#!/usr/bin/env node

import { createInterface } from 'node:readline/promises';
import { stdin as input, stdout as output } from 'node:process';
import { query } from '@anthropic-ai/claude-agent-sdk';
import {
  PermissionBroker,
  buildPermissionRequest,
  isAbortError,
  isRunRequest,
  isTypedMessage,
  mergeMandatoryHooks,
  parseNDJSONLine,
  parseRunRequest,
  permissionResponseToResult,
} from './lib/protocol.mjs';

const MIN_NODE_MAJOR = 20;
const nodeMajor = Number(process.versions.node.split('.')[0]);
if (nodeMajor < MIN_NODE_MAJOR) {
  process.stderr.write(
    `claude-agent-sdk sidecar requires Node.js >= ${MIN_NODE_MAJOR} (got ${process.versions.node}); SDK needs >=18\n`,
  );
  process.exit(1);
}

/**
 * @param {unknown} value
 */
export function writeLine(value) {
  output.write(`${JSON.stringify(value)}\n`);
}

/**
 * @param {AsyncIterable<string>} lines
 * @param {import('./lib/protocol.mjs').PermissionBroker} broker
 * @param {AbortController} abortController
 * @param {(line: string, error: unknown) => void} onUnknownLine
 */
export async function consumePermissionResponses(lines, broker, abortController, onUnknownLine) {
  const aborted = new Promise((resolve) => {
    if (abortController.signal.aborted) {
      resolve();
      return;
    }
    abortController.signal.addEventListener('abort', () => resolve(), { once: true });
  });

  const readLines = async () => {
    for await (const line of lines) {
      const trimmed = line.trim();
      if (!trimmed) {
        continue;
      }
      let message;
      try {
        message = parseNDJSONLine(trimmed);
      } catch (error) {
        onUnknownLine(trimmed, error);
        continue;
      }
      if (!broker.handleResponse(message)) {
        onUnknownLine(trimmed, new Error('unexpected stdin message (expected permission_response)'));
      }
    }
  };

  await Promise.race([readLines(), aborted]);
  broker.abortAll('stdin closed');
}

/**
 * @param {Record<string, unknown>} runRequest
 * @param {{
 *   write?: (value: unknown) => void,
 *   broker?: PermissionBroker,
 *   abortController?: AbortController,
 *   queryFn?: typeof query,
 * }} [deps]
 */
export async function runSidecar(runRequest, deps = {}) {
  const write = deps.write ?? writeLine;
  const broker = deps.broker ?? new PermissionBroker();
  const abortController = deps.abortController ?? new AbortController();
  const queryFn = deps.queryFn ?? query;

  const { prompt, options } = parseRunRequest(runRequest);
  const sdkOptions = {
    ...options,
    hooks: mergeMandatoryHooks(options.hooks),
    abortController,
    canUseTool: async (toolName, toolInput, ctx) => {
      const permissionRequest = buildPermissionRequest(toolName, toolInput, ctx);
      const responsePromise = broker.waitForResponse(String(ctx.requestId), ctx.signal);
      write(permissionRequest);

      let response;
      try {
        response = await responsePromise;
      } catch (error) {
        if (isAbortError(error)) {
          return {
            behavior: 'deny',
            message: 'permission request aborted',
            toolUseID: ctx.toolUseID,
          };
        }
        throw error;
      }

      return permissionResponseToResult(response, toolInput, toolName);
    },
  };

  const stream = queryFn({ prompt, options: sdkOptions });
  try {
    for await (const message of stream) {
      write(message);
    }
  } finally {
    broker.abortAll('query finished');
    if (typeof stream.close === 'function') {
      stream.close();
    }
  }
}

/**
 * @param {AsyncIterator<string>} iterator
 * @param {AbortSignal} signal
 * @returns {Promise<IteratorResult<string>>}
 */
async function nextLineOrAbort(iterator, signal) {
  if (signal.aborted) {
    return { done: true, value: undefined };
  }
  return new Promise((resolve, reject) => {
    const onAbort = () => resolve({ done: true, value: undefined });
    signal.addEventListener('abort', onAbort, { once: true });
    iterator.next().then(
      (result) => {
        signal.removeEventListener('abort', onAbort);
        resolve(result);
      },
      (error) => {
        signal.removeEventListener('abort', onAbort);
        reject(error);
      },
    );
  });
}

/**
 * @param {{
 *   lines?: AsyncIterable<string>,
 *   run?: typeof runSidecar,
 *   onFatal?: (error: unknown) => void,
 * }} [deps]
 */
export async function main(deps = {}) {
  const broker = new PermissionBroker();
  const abortController = new AbortController();

  const onShutdown = () => {
    broker.abortAll('SIGTERM');
    abortController.abort();
  };
  process.once('SIGTERM', onShutdown);
  process.once('SIGINT', onShutdown);

  /** @type {import('node:readline/promises').Interface | null} */
  let rl = null;
  const lineSource =
    deps.lines ??
    (async function* () {
      rl = createInterface({ input, terminal: false, crlfDelay: Infinity });
      try {
        for await (const line of rl) {
          yield line;
        }
      } finally {
        rl.close();
      }
    })();

  const iterator = lineSource[Symbol.asyncIterator]();
  let runRequest;
  try {
    const first = await iterator.next();
    if (first.done || !first.value) {
      throw new Error('missing run_request on stdin');
    }
    runRequest = parseNDJSONLine(first.value);
    if (!isTypedMessage(runRequest) || !isRunRequest(runRequest)) {
      throw new Error('first stdin line must be a run_request');
    }
  } catch (error) {
    (deps.onFatal ?? ((err) => {
      writeLine({ type: 'error', message: err instanceof Error ? err.message : String(err) });
      process.exitCode = 1;
    }))(error);
    return;
  }

  const remainingLines = {
    async *[Symbol.asyncIterator]() {
      while (true) {
        const next = await nextLineOrAbort(iterator, abortController.signal);
        if (next.done) {
          return;
        }
        yield next.value;
      }
    },
  };

  const readerPromise = consumePermissionResponses(
    remainingLines,
    broker,
    abortController,
    (line, error) => {
      writeLine({
        type: 'warning',
        message: error instanceof Error ? error.message : String(error),
        line,
      });
    },
  );

  try {
    await (deps.run ?? runSidecar)(runRequest, { broker, abortController });
  } catch (error) {
    (deps.onFatal ?? ((err) => {
      writeLine({ type: 'error', message: err instanceof Error ? err.message : String(err) });
      process.exitCode = 1;
    }))(error);
  } finally {
    if (rl) {
      rl.close();
    }
    broker.abortAll('sidecar exit');
    abortController.abort();
    await readerPromise.catch(() => {});
    void iterator.return?.().catch(() => {});
    process.off('SIGTERM', onShutdown);
    process.off('SIGINT', onShutdown);
  }
}

if (import.meta.url === new URL(process.argv[1], 'file:').href) {
  main().catch((error) => {
    writeLine({ type: 'error', message: error instanceof Error ? error.message : String(error) });
    process.exitCode = 1;
  });
}
