"""Preserve previous local logs before application-driven resets."""
from pathlib import Path
from time import time_ns


def archive_log(path):
    path = Path(path)
    if not path.exists() or not path.stat().st_size:
        return None
    folder = path.parent / 'archive'
    folder.mkdir(exist_ok=True)
    destination = folder / f'{path.stem}-{time_ns()}{path.suffix}'
    path.rename(destination)
    return destination
