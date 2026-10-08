# Leonardo image generation

Status: implemented. Entry point: `internal/tools/integrationtools/leonardo.go`.

The `leonardo_generate_image` tool submits one generation, polls until completion,
and downloads images for local previews. An enabled Leonardo integration must
contain an API key and sufficient **API** tokens. Web-app subscription credits
must not be assumed to fund API requests.

## Requests

- A UUID `model_id` uses `POST /api/rest/v1/generations`.
- Named partner models, such as `gemini-2.5-flash-image` (Nano Banana) and
  `gpt-image-1.5`, use `POST /api/rest/v2/generations` with `model`, `parameters`
  (`prompt`, `width`, `height`, `quantity`), and `public: false`.
- GPT Image requests use `quality: HIGH`, not the retired `mode` parameter.
- Nano Banana dimensions snap independently to the nearest allowed width/height.
  For a landscape concept board, request `1344 × 768`. The live API validation
  rejects width `1536` and height `1024`; maintain the dimension table when the
  provider changes its accepted values.
- Negative prompts for v2 are appended to the main prompt as an `Avoid:` clause.
  Preset style is only supplied to v1.

## Failures

Leonardo may respond with HTTP 200 and a GraphQL-style JSON array containing
`extensions.code`, `extensions.statusCode`, `extensions.details`, and `message`.
The tool checks these envelopes both during submission and status polling, before
looking for a generation ID or waiting again. Validation errors prefer
`details.message`; billing errors preserve the top-level `Insufficient tokens`
message. A rejected submission never starts polling.

If a response says `Insufficient tokens` (embedded status 402), add API credits in
Leonardo or select another funded integration. Retrying prompts or sizes does not
resolve an exhausted balance. After updating this tool's code, rebuild and restart
the serving Brute process before expecting a running session to use the fix.
No integration secrets are included in diagnostics or test fixtures.

## Verification

```sh
go test -race ./internal/tools/integrationtools -run TestLeonardo -count=1
```

Tests cover successful v1/v2 submission, downloads, terminal generation failure,
HTTP-200 validation/billing error envelopes, polling error envelopes, and the
current Nano Banana size mapping. These mock tests do not spend provider tokens
or prove a funded end-to-end generation.

Provider references: [Nano Banana](https://docs.leonardo.ai/docs/nano-banana),
[GPT Image 1.5](https://docs.leonardo.ai/docs/gpt-image-1-5), and
[async generation schema](https://docs.leonardo.ai/reference/creategeneration).
