# Mock video backend

A stand-in for a real video generation server. It speaks the async video API
shape (the same one OpenAI and vLLM-Omni use), but instead of running a model it
waits, sometimes fails, and then hands back a small test-pattern mp4.

It is **slow, has limited capacity, and fails on purpose**. That is the point:
your gateway has to cope.

## Run

```bash
docker build -t mock-video-backend .
docker run --rm -p 8000:8000 mock-video-backend
```

For faster local iteration:

```bash
docker run --rm -p 8000:8000 \
  -e MOCK_MIN_SECONDS=3 -e MOCK_MAX_SECONDS=6 \
  mock-video-backend
```

## API

| Method | Path | Notes |
|---|---|---|
| `POST` | `/v1/videos` | JSON body: `{"model","prompt","seconds","size"}`. Returns the job immediately with `status: "queued"`. `429` when the queue is full (`Retry-After` set). `400` on bad `seconds`/`size`, `404` on unknown `model`. |
| `GET` | `/v1/videos/{id}` | Job object with `status` (`queued` → `in_progress` → `completed` \| `failed`), `progress` 0–100, and `error` when failed. |
| `GET` | `/v1/videos/{id}/content` | The mp4 bytes (`video/mp4`). `409` while not completed or if the job failed, `410` once the backend has deleted its copy, `404` if unknown. |
| `DELETE` | `/v1/videos/{id}` | Cancels/deletes. Best effort: don't build on it. |
| `GET` | `/healthz` | Liveness + active job count. |

Job object:

```json
{
  "id": "video_3f9c…", "object": "video", "model": "minimax-h3-mock",
  "status": "in_progress", "progress": 42,
  "created_at": 1789900000, "completed_at": null, "expires_at": null,
  "seconds": "5", "size": "864x480", "error": null
}
```

Errors are always `{"error": {"code": "...", "message": "..."}}`.

Accepted values: `model` = `minimax-h3-mock`; `seconds` ∈ `4, 5, 8, 12`;
`size` ∈ `864x480, 480x864, 1280x720, 720x1280`. `input_reference` is accepted
and ignored.



## Example requests via curl

# Health check
curl.exe http://localhost:8000/healthz
# Create video job
curl.exe -X POST http://localhost:8000/v1/videos -H "Content-Type: application/json" -d '@test_request.json'
# Check job status
curl.exe http://localhost:8000/v1/videos/YOUR_JOB_ID
# Download completed video
curl.exe http://localhost:8000/v1/videos/YOUR_JOB_ID/content -o test_result.mp4
# Delete job
curl.exe -X DELETE http://localhost:8000/v1/videos/YOUR_JOB_ID

curl.exe http://localhost:8000/v1/videos/video_9ce0f29b16364e658f169b16
curl.exe http://localhost:8000/v1/videos/video_9ce0f29b16364e658f169b16/content -o test_result.mp4
curl.exe -X DELETE http://localhost:8000/v1/videos/video_9ce0f29b16364e658f169b16



## Behaviour you must handle

- **It's slow.** A job takes 20–60 s by default.
- **Capacity.** Only `MOCK_CAPACITY` jobs run at once; the rest sit in `queued`.
  Beyond `MOCK_MAX_QUEUE` outstanding jobs, `POST` returns `429`.
- **Failures.** Around 20 % of jobs fail *partway through* (`status: "failed"`,
  `error` populated). Submission succeeding tells you nothing.
- **The backend forgets.** It deletes its copy of a finished video after
  `MOCK_RETENTION_SECONDS` (5 min). After that `/content` is `410` and the job
  disappears from `GET`. If your gateway needs the file for an hour, copy it out.
- **Restarts lose everything.** State is in memory.

Optional nastiness (off by default, turn on when your happy path works):

- `MOCK_FLAKY_RATE=0.2` — 20 % of `GET` status/content calls return `503` with
  `Retry-After`. Retry them; don't mark the job failed.
- `MOCK_CHUNKED_CONTENT=1` — `/content` streams without `Content-Length`.

## Knobs

| Env | Default | Meaning |
|---|---|---|
| `MOCK_MIN_SECONDS` / `MOCK_MAX_SECONDS` | `20` / `60` | Job duration range |
| `MOCK_CAPACITY` | `2` | Concurrent jobs |
| `MOCK_MAX_QUEUE` | `10` | Outstanding jobs before `429` |
| `MOCK_FAIL_RATE` | `0.2` | Probability a job fails mid-way |
| `MOCK_RETENTION_SECONDS` | `300` | Backend keeps finished videos this long |
| `MOCK_FLAKY_RATE` | `0` | Probability of a `503` on `GET` calls |
| `MOCK_CHUNKED_CONTENT` | `0` | `1` = stream `/content` without `Content-Length` |
| `MOCK_SEED` | unset | Seed the RNG for reproducible runs |
| `MOCK_MODEL_NAME` | `minimax-h3-mock` | The one model it claims to serve |

`MOCK_STORAGE_PATH` (default `/tmp/mock-videos`) is where it writes files
internally. It is not part of the API; talk to the backend over HTTP only.

## Walkthrough

```bash
# create
curl -s localhost:8000/v1/videos -H 'content-type: application/json' \
  -d '{"model":"minimax-h3-mock","prompt":"a man walking down a street","seconds":"5","size":"864x480"}'
# -> {"id":"video_…","status":"queued",...}

# poll
curl -s localhost:8000/v1/videos/video_…
# -> ... "status":"in_progress","progress":37 ...

# download when completed
curl -s -o out.mp4 -w '%{http_code}\n' localhost:8000/v1/videos/video_…/content
```
