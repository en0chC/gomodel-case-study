"""
Mock video generation backend.

Mirrors the async shape of vLLM-Omni's / OpenAI's Videos API:

    POST   /v1/videos               -> job created (status "queued")
    GET    /v1/videos/{id}          -> status + progress
    GET    /v1/videos/{id}/content  -> the mp4 (once completed)
    DELETE /v1/videos/{id}          -> best-effort cancel/delete
    GET    /healthz

It is deliberately slow, has limited concurrency, fails some jobs on
purpose, and deletes its own copy of finished videos after a while.
All behaviour is controlled by MOCK_* environment variables (see README).
"""

import asyncio
import logging
import os
import random
import shutil
import time
import uuid
from contextlib import asynccontextmanager
from dataclasses import dataclass, field
from pathlib import Path
from typing import Optional

from fastapi import FastAPI, HTTPException, Request
from fastapi.responses import FileResponse, JSONResponse, StreamingResponse
from pydantic import BaseModel, Field

log = logging.getLogger("mock-video")
logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")


# --------------------------------------------------------------------------- #
# Configuration (all via environment)
# --------------------------------------------------------------------------- #
def _env_float(name: str, default: float) -> float:
    return float(os.environ.get(name, default))


def _env_int(name: str, default: int) -> int:
    return int(os.environ.get(name, default))


CFG = {
    # how long a job takes, uniformly random in [min, max] seconds
    "MIN_SECONDS": _env_float("MOCK_MIN_SECONDS", 20),
    "MAX_SECONDS": _env_float("MOCK_MAX_SECONDS", 60),
    # how many jobs may be in_progress at once (queued beyond that)
    "CAPACITY": _env_int("MOCK_CAPACITY", 2),
    # how many may be queued+in_progress before POST returns 429
    "MAX_QUEUE": _env_int("MOCK_MAX_QUEUE", 10),
    # probability a job fails partway through generation
    "FAIL_RATE": _env_float("MOCK_FAIL_RATE", 0.2),
    # backend deletes its copy of a finished video after this many seconds
    "RETENTION_SECONDS": _env_float("MOCK_RETENTION_SECONDS", 300),
    # probability that a GET status / content call returns 503 (retry me)
    "FLAKY_RATE": _env_float("MOCK_FLAKY_RATE", 0.0),
    # if 1, /content streams without Content-Length (chunked)
    "CHUNKED_CONTENT": _env_int("MOCK_CHUNKED_CONTENT", 0),
    # where finished mp4s are written (internal detail; not part of the API)
    "STORAGE_PATH": os.environ.get("MOCK_STORAGE_PATH", "/tmp/mock-videos"),
    # the sample mp4 that every job "produces"
    "SAMPLE_MP4": os.environ.get("MOCK_SAMPLE_MP4", str(Path(__file__).with_name("sample.mp4"))),
    "SEED": os.environ.get("MOCK_SEED"),
}

ALLOWED_SECONDS = {"4", "5", "8", "12"}
ALLOWED_SIZES = {"864x480", "480x864", "1280x720", "720x1280"}
MODEL_NAME = os.environ.get("MOCK_MODEL_NAME", "minimax-h3-mock")

rng = random.Random(CFG["SEED"]) if CFG["SEED"] else random.Random()


# --------------------------------------------------------------------------- #
# State
# --------------------------------------------------------------------------- #
@dataclass
class Job:
    id: str
    model: str
    prompt: str
    seconds: str
    size: str
    created_at: int
    status: str = "queued"  # queued | in_progress | completed | failed
    progress: int = 0
    completed_at: Optional[int] = None
    expires_at: Optional[int] = None
    error: Optional[dict] = None
    path: Optional[Path] = None
    task: Optional[asyncio.Task] = field(default=None, repr=False)

    def to_dict(self) -> dict:
        d = {
            "id": self.id,
            "object": "video",
            "model": self.model,
            "status": self.status,
            "progress": self.progress,
            "created_at": self.created_at,
            "completed_at": self.completed_at,
            "expires_at": self.expires_at,
            "seconds": self.seconds,
            "size": self.size,
            "error": self.error,
        }
        return d


JOBS: dict[str, Job] = {}
SEM: asyncio.Semaphore  # created at startup


class CreateVideoRequest(BaseModel):
    model: str = Field(default=MODEL_NAME)
    prompt: str = Field(min_length=1, max_length=4000)
    seconds: str = "5"
    size: str = "864x480"
    input_reference: Optional[str] = None  # accepted but ignored by the mock


# --------------------------------------------------------------------------- #
# Worker
# --------------------------------------------------------------------------- #
async def run_job(job: Job) -> None:
    storage = Path(CFG["STORAGE_PATH"])
    try:
        async with SEM:
            job.status = "in_progress"
            duration = rng.uniform(CFG["MIN_SECONDS"], CFG["MAX_SECONDS"])
            will_fail = rng.random() < CFG["FAIL_RATE"]
            fail_at = rng.uniform(0.2, 0.9) if will_fail else None
            log.info("job %s started (%.1fs, will_fail=%s)", job.id, duration, will_fail)

            started = time.monotonic()
            while True:
                elapsed = time.monotonic() - started
                frac = min(elapsed / duration, 1.0)
                job.progress = int(frac * 100)
                if fail_at is not None and frac >= fail_at:
                    raise RuntimeError("generation failed: CUDA out of memory (simulated)")
                if frac >= 1.0:
                    break
                await asyncio.sleep(0.5)

            storage.mkdir(parents=True, exist_ok=True)
            dst = storage / f"{job.id}.mp4"
            shutil.copyfile(CFG["SAMPLE_MP4"], dst)
            now = int(time.time())
            job.path = dst
            job.progress = 100
            job.status = "completed"
            job.completed_at = now
            job.expires_at = now + int(CFG["RETENTION_SECONDS"])
            log.info("job %s completed", job.id)

        # retention: backend forgets the file after a while
        await asyncio.sleep(CFG["RETENTION_SECONDS"])
        _delete_file(job)
        JOBS.pop(job.id, None)
        log.info("job %s expired and removed", job.id)

    except asyncio.CancelledError:
        job.status = "failed"
        job.error = {"code": "cancelled", "message": "job was cancelled"}
        _delete_file(job)
        raise
    except Exception as e:  # noqa: BLE001
        job.status = "failed"
        job.progress = min(job.progress, 99)
        job.error = {"code": "generation_error", "message": str(e)}
        job.completed_at = int(time.time())
        job.expires_at = job.completed_at + int(CFG["RETENTION_SECONDS"])
        log.warning("job %s failed: %s", job.id, e)
        # failed jobs are forgotten after the same retention window
        await asyncio.sleep(CFG["RETENTION_SECONDS"])
        JOBS.pop(job.id, None)


def _delete_file(job: Job) -> None:
    if job.path and job.path.exists():
        try:
            job.path.unlink()
        except OSError:
            pass
    job.path = None


# --------------------------------------------------------------------------- #
# App
# --------------------------------------------------------------------------- #
@asynccontextmanager
async def lifespan(app: FastAPI):
    global SEM
    SEM = asyncio.Semaphore(CFG["CAPACITY"])
    if not Path(CFG["SAMPLE_MP4"]).exists():
        raise RuntimeError(f"sample mp4 not found at {CFG['SAMPLE_MP4']}")
    shutil.rmtree(CFG["STORAGE_PATH"], ignore_errors=True)
    Path(CFG["STORAGE_PATH"]).mkdir(parents=True, exist_ok=True)
    log.info("mock video backend up; config=%s", {k: v for k, v in CFG.items() if k != "SEED"})
    yield
    for j in list(JOBS.values()):
        if j.task and not j.task.done():
            j.task.cancel()


app = FastAPI(title="Mock Video Backend", lifespan=lifespan)


def _maybe_flake() -> None:
    if CFG["FLAKY_RATE"] > 0 and rng.random() < CFG["FLAKY_RATE"]:
        raise HTTPException(
            status_code=503,
            detail={"code": "backend_unavailable", "message": "temporarily unavailable, retry"},
            headers={"Retry-After": "2"},
        )


def _get_job(video_id: str) -> Job:
    job = JOBS.get(video_id)
    if job is None:
        raise HTTPException(404, {"code": "not_found", "message": f"video {video_id} not found"})
    return job


@app.get("/healthz")
async def healthz():
    active = sum(1 for j in JOBS.values() if j.status in ("queued", "in_progress"))
    return {"ok": True, "active_jobs": active, "capacity": CFG["CAPACITY"]}


@app.post("/v1/videos", status_code=200)
async def create_video(req: CreateVideoRequest):
    if req.seconds not in ALLOWED_SECONDS:
        raise HTTPException(400, {"code": "invalid_request", "message": f"seconds must be one of {sorted(ALLOWED_SECONDS)}"})
    if req.size not in ALLOWED_SIZES:
        raise HTTPException(400, {"code": "invalid_request", "message": f"size must be one of {sorted(ALLOWED_SIZES)}"})
    if req.model != MODEL_NAME:
        raise HTTPException(404, {"code": "model_not_found", "message": f"unknown model {req.model!r}; this backend serves {MODEL_NAME!r}"})

    pending = sum(1 for j in JOBS.values() if j.status in ("queued", "in_progress"))
    if pending >= CFG["MAX_QUEUE"]:
        raise HTTPException(
            429,
            {"code": "over_capacity", "message": "backend queue is full, retry later"},
            headers={"Retry-After": "5"},
        )

    job = Job(
        id=f"video_{uuid.uuid4().hex[:24]}",
        model=req.model,
        prompt=req.prompt,
        seconds=req.seconds,
        size=req.size,
        created_at=int(time.time()),
    )
    JOBS[job.id] = job
    job.task = asyncio.create_task(run_job(job))
    log.info("job %s queued", job.id)
    return job.to_dict()


@app.get("/v1/videos/{video_id}")
async def get_video(video_id: str):
    _maybe_flake()
    return _get_job(video_id).to_dict()


@app.get("/v1/videos/{video_id}/content")
async def get_content(video_id: str, request: Request):
    _maybe_flake()
    job = _get_job(video_id)
    if job.status == "failed":
        raise HTTPException(409, {"code": "job_failed", "message": "video generation failed", "error": job.error})
    if job.status != "completed":
        raise HTTPException(409, {"code": "not_ready", "message": f"video is {job.status}"})
    if not job.path or not job.path.exists():
        raise HTTPException(410, {"code": "expired", "message": "video content has been deleted"})

    if CFG["CHUNKED_CONTENT"]:
        def _iter():
            with open(job.path, "rb") as f:
                while chunk := f.read(64 * 1024):
                    yield chunk
        return StreamingResponse(_iter(), media_type="video/mp4",
                                 headers={"Content-Disposition": f'attachment; filename="{job.id}.mp4"'})
    return FileResponse(job.path, media_type="video/mp4", filename=f"{job.id}.mp4")


@app.delete("/v1/videos/{video_id}")
async def delete_video(video_id: str):
    job = _get_job(video_id)
    if job.task and not job.task.done():
        job.task.cancel()
        try:
            await asyncio.wait_for(asyncio.shield(job.task), timeout=2.0)
        except (asyncio.CancelledError, asyncio.TimeoutError, Exception):  # noqa: BLE001
            pass
    _delete_file(job)
    JOBS.pop(video_id, None)
    return {"id": video_id, "object": "video", "deleted": True}


@app.exception_handler(HTTPException)
async def http_exc(_: Request, exc: HTTPException):
    detail = exc.detail if isinstance(exc.detail, dict) else {"code": "error", "message": str(exc.detail)}
    return JSONResponse({"error": detail}, status_code=exc.status_code, headers=exc.headers)
