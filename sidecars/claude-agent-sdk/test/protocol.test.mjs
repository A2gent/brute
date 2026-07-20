import assert from 'node:assert/strict';
import test from 'node:test';
import {
  MSG_PERMISSION_REQUEST,
  MSG_PERMISSION_RESPONSE,
  MSG_RUN_REQUEST,
  PermissionBroker,
  buildAskUserUpdatedInput,
  buildPermissionRequest,
  isAbortError,
  parseNDJSONLine,
  parseRunRequest,
  permissionResponseToResult,
  sanitizeQueryOptions,
} from '../lib/protocol.mjs';

test('parseNDJSONLine rejects empty and non-object lines', () => {
  assert.throws(() => parseNDJSONLine(''), /empty NDJSON line/);
  assert.throws(() => parseNDJSONLine('[]'), /JSON object/);
});

test('sanitizeQueryOptions strips allowedTools and defaults permissionMode', () => {
  const out = sanitizeQueryOptions({
    allowedTools: ['Bash', 'Read'],
    cwd: '/tmp',
    permissionMode: '',
  });
  assert.deepEqual(out, { cwd: '/tmp', permissionMode: 'default' });
  assert.equal('allowedTools' in out, false);
});

test('sanitizeQueryOptions rejects auto-approve permission modes for bridge', () => {
  for (const mode of ['bypassPermissions', 'acceptEdits', 'auto']) {
    const out = sanitizeQueryOptions({ permissionMode: mode, cwd: '/tmp' });
    assert.equal(out.permissionMode, 'default', `expected default for ${mode}`);
    assert.equal(out.cwd, '/tmp');
  }
  assert.equal(sanitizeQueryOptions({ permissionMode: 'plan' }).permissionMode, 'plan');
});

test('parseRunRequest requires run_request with string prompt', () => {
  const parsed = parseRunRequest({
    type: MSG_RUN_REQUEST,
    prompt: 'hello',
    options: { allowedTools: ['Bash'] },
  });
  assert.equal(parsed.prompt, 'hello');
  assert.equal(parsed.options.permissionMode, 'default');
  assert.equal('allowedTools' in parsed.options, false);
});

test('buildPermissionRequest maps canUseTool context', () => {
  const req = buildPermissionRequest(
    'Bash',
    { command: 'ls' },
    {
      requestId: 'req-1',
      toolUseID: 'tu-1',
      title: 'Run ls',
      displayName: 'Bash',
      description: 'list files',
      suggestions: [{ type: 'addRules', rules: [], behavior: 'allow', destination: 'session' }],
    },
  );
  assert.equal(req.type, MSG_PERMISSION_REQUEST);
  assert.equal(req.requestId, 'req-1');
  assert.equal(req.toolUseID, 'tu-1');
  assert.equal(req.toolName, 'Bash');
  assert.deepEqual(req.input, { command: 'ls' });
  assert.equal(req.title, 'Run ls');
});

test('permissionResponseToResult allow and deny', () => {
  const allow = permissionResponseToResult(
    {
      type: MSG_PERMISSION_RESPONSE,
      requestId: 'req-1',
      behavior: 'allow',
      updatedInput: { command: 'pwd' },
    },
    { command: 'ls' },
    'Bash',
  );
  assert.equal(allow.behavior, 'allow');
  assert.deepEqual(allow.updatedInput, { command: 'pwd' });

  const deny = permissionResponseToResult(
    {
      type: MSG_PERMISSION_RESPONSE,
      requestId: 'req-2',
      behavior: 'deny',
      message: 'nope',
    },
    {},
    'Bash',
  );
  assert.equal(deny.behavior, 'deny');
  assert.equal(deny.message, 'nope');
});

test('buildAskUserUpdatedInput merges answers from permission_response', () => {
  const original = {
    questions: [{ question: 'Pick one?', header: 'Pick', options: [], multiSelect: false }],
  };
  const updated = buildAskUserUpdatedInput(original, {
    answers: { 'Pick one?': 'A' },
  });
  assert.deepEqual(updated.answers, { 'Pick one?': 'A' });
  assert.equal(updated.questions, original.questions);

  const viaResult = permissionResponseToResult(
    {
      type: MSG_PERMISSION_RESPONSE,
      requestId: 'req-3',
      behavior: 'allow',
      answers: { 'Pick one?': 'A' },
    },
    original,
    'AskUserQuestion',
  );
  assert.equal(viaResult.behavior, 'allow');
  assert.deepEqual(viaResult.updatedInput?.answers, { 'Pick one?': 'A' });
});

test('PermissionBroker resolves matching response and rejects abort', async () => {
  const broker = new PermissionBroker();
  const controller = new AbortController();
  const wait = broker.waitForResponse('req-1', controller.signal);
  assert.equal(
    broker.handleResponse({
      type: MSG_PERMISSION_RESPONSE,
      requestId: 'req-1',
      behavior: 'allow',
    }),
    true,
  );
  const response = await wait;
  assert.equal(response.requestId, 'req-1');

  const aborted = broker.waitForResponse('req-2', controller.signal);
  controller.abort();
  await assert.rejects(aborted, (error) => isAbortError(error));
});

test('PermissionBroker abortAll rejects pending waits', async () => {
  const broker = new PermissionBroker();
  const controller = new AbortController();
  const wait = broker.waitForResponse('req-9', controller.signal);
  broker.abortAll('shutdown');
  await assert.rejects(wait, (error) => isAbortError(error));
  assert.equal(broker.size, 0);
});
