import assert from 'node:assert/strict';
import test from 'node:test';
import { PermissionBroker } from '../lib/protocol.mjs';
import { consumePermissionResponses, runSidecar } from '../index.mjs';

test('runSidecar injects mandatory PreToolUse hook with ask for every tool', async () => {
  const broker = new PermissionBroker();
  const abortController = new AbortController();
  const hookResults = [];

  const queryFn = ({ options }) => {
    const matchers = options.hooks?.PreToolUse;
    assert.ok(Array.isArray(matchers));
    const policyMatcher = matchers[0];
    assert.equal(policyMatcher.matcher, undefined);
    assert.equal(typeof options.canUseTool, 'function');

    return {
      async *[Symbol.asyncIterator]() {
        const result = await policyMatcher.hooks[0](
          {
            hook_event_name: 'PreToolUse',
            tool_name: 'Read',
            tool_input: { file_path: '/tmp/x' },
            tool_use_id: 'tu-read',
          },
          'tu-read',
          { signal: abortController.signal },
        );
        hookResults.push(result);
        yield { type: 'result', subtype: 'success', result: 'done' };
      },
      close() {},
    };
  };

  await runSidecar(
    {
      type: 'run_request',
      prompt: 'test',
      options: { hooks: { PreToolUse: [] } },
    },
    { broker, abortController, write: () => {}, queryFn },
  );

  assert.deepEqual(hookResults[0], {
    hookSpecificOutput: {
      hookEventName: 'PreToolUse',
      permissionDecision: 'ask',
      permissionDecisionReason: 'A2gent requires per-tool approval',
    },
  });
});

test('runSidecar emits permission_request and maps allow response through canUseTool', async () => {
  const broker = new PermissionBroker();
  const abortController = new AbortController();
  const stdout = [];
  const permissionRequests = [];

  const queryFn = ({ options }) => ({
    async *[Symbol.asyncIterator]() {
      const result = await options.canUseTool(
        'Bash',
        { command: 'echo hi' },
        {
          requestId: 'sdk-req-1',
          toolUseID: 'tu-1',
          signal: abortController.signal,
          title: 'Run echo',
        },
      );
      permissionRequests.push(result);
      yield { type: 'result', subtype: 'success', result: 'done' };
    },
    close() {},
  });

  const reader = consumePermissionResponses(
    (async function* () {
      yield JSON.stringify({
        type: 'permission_response',
        requestId: 'sdk-req-1',
        behavior: 'allow',
        updatedInput: { command: 'echo hi' },
      });
    })(),
    broker,
    abortController,
    () => {},
  );

  await runSidecar(
    { type: 'run_request', prompt: 'test', options: { allowedTools: ['Bash'] } },
    { broker, abortController, write: (msg) => stdout.push(msg), queryFn },
  );
  await reader;

  assert.equal(stdout.length, 2);
  assert.equal(stdout[0].type, 'permission_request');
  assert.equal(stdout[0].requestId, 'sdk-req-1');
  assert.equal(stdout[1].type, 'result');
  assert.equal(permissionRequests[0].behavior, 'allow');
  assert.deepEqual(permissionRequests[0].updatedInput, { command: 'echo hi' });
});

test('runSidecar maps deny on abort signal', async () => {
  const broker = new PermissionBroker();
  const abortController = new AbortController();
  const results = [];

  const queryFn = ({ options }) => ({
    async *[Symbol.asyncIterator]() {
      const controller = new AbortController();
      controller.abort();
      const result = await options.canUseTool(
        'Write',
        { file_path: '/tmp/x' },
        {
          requestId: 'sdk-req-2',
          toolUseID: 'tu-2',
          signal: controller.signal,
        },
      );
      results.push(result);
      yield { type: 'result', subtype: 'success', result: 'ok' };
    },
    close() {},
  });

  await runSidecar(
    { type: 'run_request', prompt: 'test' },
    { broker, abortController, write: () => {}, queryFn },
  );

  assert.equal(results[0].behavior, 'deny');
  assert.match(results[0].message, /aborted/i);
});

test('runSidecar maps AskUserQuestion answers into updatedInput', async () => {
  const broker = new PermissionBroker();
  const abortController = new AbortController();
  const results = [];

  const queryFn = ({ options }) => ({
    async *[Symbol.asyncIterator]() {
      const input = {
        questions: [
          {
            question: 'Color?',
            header: 'Color',
            options: [{ label: 'Blue', description: 'b' }],
            multiSelect: false,
          },
        ],
      };
      const wait = options.canUseTool('AskUserQuestion', input, {
        requestId: 'sdk-req-3',
        toolUseID: 'tu-3',
        signal: abortController.signal,
      });
      broker.handleResponse({
        type: 'permission_response',
        requestId: 'sdk-req-3',
        behavior: 'allow',
        answers: { 'Color?': 'Blue' },
      });
      results.push(await wait);
      yield { type: 'result', subtype: 'success', result: 'ok' };
    },
    close() {},
  });

  await runSidecar(
    { type: 'run_request', prompt: 'test' },
    { broker, abortController, write: () => {}, queryFn },
  );

  assert.equal(results[0].behavior, 'allow');
  assert.deepEqual(results[0].updatedInput?.answers, { 'Color?': 'Blue' });
});

test('runSidecar resolves permission when write callback responds immediately', async () => {
  const broker = new PermissionBroker();
  const abortController = new AbortController();
  const results = [];

  const queryFn = ({ options }) => ({
    async *[Symbol.asyncIterator]() {
      const result = await options.canUseTool(
        'Bash',
        { command: 'echo fast' },
        {
          requestId: 'sdk-req-fast',
          toolUseID: 'tu-fast',
          signal: abortController.signal,
        },
      );
      results.push(result);
      yield { type: 'result', subtype: 'success', result: 'ok' };
    },
    close() {},
  });

  await runSidecar(
    { type: 'run_request', prompt: 'test' },
    {
      broker,
      abortController,
      write: (msg) => {
        if (msg.type === 'permission_request') {
          broker.handleResponse({
            type: 'permission_response',
            requestId: msg.requestId,
            behavior: 'allow',
            updatedInput: msg.input,
          });
        }
      },
      queryFn,
    },
  );

  assert.equal(results[0].behavior, 'allow');
  assert.deepEqual(results[0].updatedInput, { command: 'echo fast' });
});

test('main exits after query when stdin lines never end', async () => {
  const { main } = await import('../index.mjs');
  let releaseLine;
  const lineGate = new Promise((resolve) => {
    releaseLine = resolve;
  });

  const lines = (async function* () {
    yield JSON.stringify({ type: 'run_request', prompt: 'hello' });
    await lineGate;
    yield 'never reached';
  })();

  const queryFn = ({ options }) => ({
    async *[Symbol.asyncIterator]() {
      await options.canUseTool(
        'Bash',
        { command: 'noop' },
        {
          requestId: 'sdk-req-hang',
          toolUseID: 'tu-hang',
          signal: new AbortController().signal,
        },
      );
      yield { type: 'result', subtype: 'success', result: 'done' };
    },
    close() {},
  });

  let mainSettled = false;
  const mainPromise = main({
    lines,
    run: async (runRequest, deps) => {
      deps.queryFn = queryFn;
      deps.write = (msg) => {
        if (msg.type === 'permission_request') {
          deps.broker.handleResponse({
            type: 'permission_response',
            requestId: msg.requestId,
            behavior: 'deny',
            message: 'no',
          });
        }
      };
      await runSidecar(runRequest, deps);
    },
  }).finally(() => {
    mainSettled = true;
  });

  await Promise.race([
    mainPromise,
    new Promise((_, reject) => setTimeout(() => reject(new Error('main hung')), 2000)),
  ]);

  assert.equal(mainSettled, true);
  releaseLine();
});

test('main reads run_request then permission_response from stdin lines', async () => {
  const { main } = await import('../index.mjs');
  const stdout = [];
  const lines = (async function* () {
    yield JSON.stringify({ type: 'run_request', prompt: 'hello' });
    yield JSON.stringify({
      type: 'permission_response',
      requestId: 'sdk-req-main',
      behavior: 'deny',
      message: 'blocked',
    });
  })();

  const queryFn = ({ options }) => ({
    async *[Symbol.asyncIterator]() {
      const result = await options.canUseTool(
        'Bash',
        { command: 'rm -rf /' },
        {
          requestId: 'sdk-req-main',
          toolUseID: 'tu-main',
          signal: new AbortController().signal,
        },
      );
      yield { type: 'result', subtype: 'success', result: result.behavior };
    },
    close() {},
  });

  await main({
    lines,
    run: async (runRequest, deps) => {
      deps.write = (msg) => stdout.push(msg);
      deps.queryFn = queryFn;
      await runSidecar(runRequest, deps);
    },
  });

  assert.equal(stdout[0].type, 'permission_request');
  assert.equal(stdout[1].type, 'result');
  assert.equal(stdout[1].result, 'deny');
});
