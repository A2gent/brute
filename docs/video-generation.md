# Video generation with MiniMax

The `minimax` integration lets an agent create videos with the MiniMax Hailuo API.

## Setup

1. Create an API key at platform.minimax.io (accounts in China: platform.minimaxi.com and set the API base URL to `https://api.minimaxi.com`).
2. In Caesar open Integrations, add MiniMax and paste the key. Use Test to verify it.

## Tool

`minimax_generate_video` accepts:

- `prompt`: text description, camera moves as `[Pan left]`, `[Push in]`, `[Static shot]`.
- `image`: optional first frame (local path or URL) for image-to-video.
- `model`, `duration` (6 or 10), `resolution` (768P or 1080P), `prompt_optimizer`.
- `task_id`: resume waiting for a previous task instead of creating a new one.

The tool creates the task, polls until it finishes, downloads the MP4 into `generated/minimax/` and returns
`video_file` and `artifacts` metadata. Caesar plays the video inline in the tool call block.
