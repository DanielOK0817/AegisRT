"""Offline model/import and read-only database checks for the isolated lab."""
from __future__ import annotations

import argparse
import asyncio
import contextlib
import gc
import hashlib
import importlib
import io
import json
import os
from pathlib import Path
import platform
import resource
import socket
import sys
from datetime import datetime, timezone


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source-dir", default="/app")
    parser.add_argument("--env-file", default="/app/.env")
    parser.add_argument("--output", default="/work/evidence/preflight.json")
    args = parser.parse_args()
    source = Path(args.source_dir).resolve()
    sys.path.insert(0, str(source))
    os.chdir(source)
    env_file = Path(args.env_file)
    for line in env_file.read_text(encoding="utf-8-sig").splitlines():
        if line.strip() and not line.lstrip().startswith("#") and "=" in line:
            key, value = line.split("=", 1)
            os.environ[key.strip()] = value.strip().strip("\"'")
    os.environ["HF_HUB_OFFLINE"] = "1"
    os.environ["TRANSFORMERS_OFFLINE"] = "1"
    os.environ["TOKENIZERS_PARALLELISM"] = "false"
    os.environ["OMP_NUM_THREADS"] = "1"
    result = {
        "checked_at": datetime.now(timezone.utc).isoformat(),
        "host_kernel": platform.release(),
        "container_userspace": Path("/etc/os-release").read_text(encoding="utf-8"),
        "python": platform.python_version(),
        "configuration_sha256": hashlib.sha256(env_file.read_bytes()).hexdigest(),
        "model_api_calls": 0,
        "checks": {},
    }
    credentials = {name: bool(os.environ.get(name)) for name in (
        "QUERY_ENGINE_API_KEY", "MEDIA_ENGINE_API_KEY", "INSIGHT_ENGINE_API_KEY",
        "FORUM_HOST_API_KEY", "REPORT_ENGINE_API_KEY", "KEYWORD_OPTIMIZER_API_KEY",
    )}
    result["checks"]["configured_credentials"] = {
        "passed": all(credentials.values()), "present": credentials,
    }

    def check(name, operation):
        # Import libraries may log endpoint configuration; keep all such output private.
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            try:
                value = operation()
                result["checks"][name] = {"passed": True, "result": value}
            except BaseException as exc:
                result["checks"][name] = {"passed": False, "error_type": type(exc).__name__}

    def imports():
        names = [f"{name}Engine.agent" for name in ("Query", "Media", "Insight")]
        for name in names:
            importlib.import_module(name)
        return names

    check("engine_imports", imports)

    async def database():
        from InsightEngine.utils.db import get_async_engine
        from sqlalchemy import text
        engine = get_async_engine()
        try:
            async with engine.connect() as connection:
                values = {}
                for table in ("bilibili_video", "bilibili_video_comment"):
                    count = await connection.execute(text(f"SELECT count(*) FROM {table}"))
                    values[table] = count.scalar_one()
                if values != {"bilibili_video": 3, "bilibili_video_comment": 127}:
                    raise ValueError("Unexpected frozen sample counts")
                return values
        finally:
            await engine.dispose()

    check("database_real_sample", lambda: asyncio.run(database()))

    def disabled_network(*args, **kwargs):
        raise RuntimeError("Network disabled during local-model verification")

    socket.socket.connect = disabled_network
    socket.create_connection = disabled_network

    def sentiment():
        import torch
        from transformers import AutoTokenizer, AutoModelForSequenceClassification
        torch.set_num_threads(1)
        path = source / "SentimentAnalysisModel/WeiboMultilingualSentiment/model"
        tokenizer = AutoTokenizer.from_pretrained(path, local_files_only=True)
        model = AutoModelForSequenceClassification.from_pretrained(path, local_files_only=True)
        model.eval()
        with torch.inference_mode():
            output = model(**tokenizer("本地模型检查", return_tensors="pt"))
        shape = list(output.logits.shape)
        del model, tokenizer, output
        gc.collect()
        return {"logits_shape": shape, "torch": torch.__version__, "cuda_available": torch.cuda.is_available()}

    check("local_sentiment_inference", sentiment)

    def clustering():
        from sentence_transformers import SentenceTransformer
        model = SentenceTransformer(os.environ.get("INSIGHT_CLUSTERING_MODEL_PATH", "/models/clustering"), local_files_only=True)
        vectors = model.encode(["第一条本地检查", "第二条本地检查"])
        shape = list(vectors.shape)
        del model, vectors
        gc.collect()
        return {"embedding_shape": shape}

    check("local_clustering_inference", clustering)
    result["max_rss_kib"] = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss
    result["passed"] = all(row["passed"] for row in result["checks"].values())
    output = Path(args.output)
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(result, ensure_ascii=False))
    return 0 if result["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
