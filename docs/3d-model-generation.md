# 3D model generation (Meshy, Tripo3D)

Two integrations let the agent turn text or images into 3D models:

| Integration | Tool | API |
|---|---|---|
| `meshy` | `meshy_generate_3d` | `https://api.meshy.ai` (text-to-3d v2, image-to-3d v1) |
| `tripo` | `tripo_generate_3d` | `https://openapi.tripo3d.ai/v3` (v2 is retired on 2026-11-01) |

Config keys: `api_key` (required), `model`, `timeout_seconds` (default 600), `base_url` (override for proxies/tests).
The integration Test button reads the credit balance to verify the key.

## Behavior

- Pass `prompt` for text-to-3D or `image` for image-to-3D. `image` accepts a local path (relative to the
  workspace or absolute), a public URL, or (Tripo only) a `task_...`/`file_...` id.
- Local images: Meshy receives a base64 data URI; Tripo receives a `file_token` from `POST /files`.
- Meshy text-to-3D is two-step: a `preview` task builds the mesh, then a `refine` task textures it
  (skipped when `texture=false`).
- The tool polls every 5s until the task finishes or the timeout expires, then downloads the model
  and a preview image into `<workdir>/generated/meshy|tripo/<task_id>.<ext>`.
- Result metadata: `model_3d` (provider, task_id, path, files, thumbnail) and `image_file` pointing at the
  preview, so Caesar shows the rendered thumbnail inline.

## Chaining example

1. `leonardo_generate_image` with a prompt like "isometric product shot of a wooden chair, plain white background".
2. `tripo_generate_3d` or `meshy_generate_3d` with `image` set to the saved PNG path.
3. Optionally `blender_run_script` to import the GLB and post-process or render it.
