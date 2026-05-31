"""FastAPI app: JSON API over the shared data layer + serves the built SPA.

A fresh SQLite connection is opened per request (sqlite connections are not
shareable across threads, and FastAPI runs sync endpoints in a threadpool).
Connections are read-mostly; the capture container is the only writer.
"""

import csv
import io
import os

from fastapi import Depends, FastAPI, HTTPException, Query
from fastapi.responses import StreamingResponse
from fastapi.staticfiles import StaticFiles

from meterfinder import db

from .schemas import MeterUpdate, TestCreate, TestStart

DB_PATH = os.environ.get("DB_PATH", "meters.db")
STATIC_DIR = os.environ.get("STATIC_DIR", "/app/static")

# rtlamr -msgtype tokens, derived from the stored outer Type string.
_MSGTYPE_MAP = {"SCM": "scm", "SCM+": "scm+", "IDM": "idm", "NetIDM": "netidm"}

app = FastAPI(title="meterfinder", version="1.0")


def get_db():
    con = db.connect(DB_PATH)
    db.init_schema(con)
    try:
        yield con
    finally:
        con.close()


# --- health ------------------------------------------------------------------
@app.get("/api/health")
def health(con=Depends(get_db)):
    return db.health(con)


# --- meters ------------------------------------------------------------------
@app.get("/api/meters")
def meters(
    con=Depends(get_db),
    since: str | None = None,
    until: str | None = None,
    msg_type: str | None = None,
    endpoint_type: int | None = None,
    electric_only: bool = False,
):
    return db.leaderboard(
        con,
        since=db.normalize_ts(since) if since else None,
        until=db.normalize_ts(until) if until else None,
        msg_type=msg_type,
        endpoint_type=endpoint_type,
        electric_only=electric_only,
    )


@app.get("/api/meters/{endpoint_id}")
def meter_detail(
    endpoint_id: int,
    con=Depends(get_db),
    since: str | None = None,
    until: str | None = None,
    bucket: str = Query("1h", pattern="^(5m|1h|1d)$"),
):
    series = db.meter_series(
        con,
        endpoint_id,
        since=db.normalize_ts(since) if since else None,
        until=db.normalize_ts(until) if until else None,
        bucket=bucket,
    )
    series["annotation"] = db.get_meter(con, endpoint_id)
    return series


@app.patch("/api/meters/{endpoint_id}")
def update_meter(endpoint_id: int, body: MeterUpdate, con=Depends(get_db)):
    return db.update_meter(con, endpoint_id, **body.model_dump(exclude_none=True))


@app.get("/api/meters/{endpoint_id}/filter-command")
def filter_command(endpoint_id: int, con=Depends(get_db)):
    row = con.execute(
        "SELECT msg_type FROM readings WHERE endpoint_id = ? "
        "AND msg_type IS NOT NULL ORDER BY ts DESC LIMIT 1",
        (endpoint_id,),
    ).fetchone()
    if not row:
        raise HTTPException(404, "meter not seen")
    token = _MSGTYPE_MAP.get(row["msg_type"], "scm")
    cmd = f"rtlamr -filterid={endpoint_id} -msgtype={token} -format=json"
    return {"endpoint_id": endpoint_id, "msg_type": row["msg_type"], "command": cmd}


@app.get("/api/meters/{endpoint_id}/export.csv")
def export_csv(
    endpoint_id: int,
    con=Depends(get_db),
    since: str | None = None,
    until: str | None = None,
):
    rows = db.meter_readings(
        con,
        endpoint_id,
        since=db.normalize_ts(since) if since else None,
        until=db.normalize_ts(until) if until else None,
    )
    buf = io.StringIO()
    w = csv.writer(buf)
    w.writerow(["ts", "msg_type", "endpoint_id", "endpoint_type", "consumption", "source"])
    for r in rows:
        w.writerow([r["ts"], r["msg_type"], r["endpoint_id"], r["endpoint_type"],
                    r["consumption"], r["source"]])
    buf.seek(0)
    return StreamingResponse(
        iter([buf.getvalue()]),
        media_type="text/csv",
        headers={"Content-Disposition": f'attachment; filename="meter_{endpoint_id}.csv"'},
    )


# --- test windows & correlation ----------------------------------------------
@app.get("/api/tests")
def list_tests(con=Depends(get_db)):
    return db.list_tests(con)


@app.post("/api/tests/start")
def start_test(body: TestStart, con=Depends(get_db)):
    return db.create_test(con, body.label, db.now_ts(), None)


@app.post("/api/tests/{test_id}/stop")
def stop_test(test_id: int, con=Depends(get_db)):
    t = db.stop_test(con, test_id, db.now_ts())
    if not t:
        raise HTTPException(404, "test not found")
    return t


@app.post("/api/tests")
def create_test(body: TestCreate, con=Depends(get_db)):
    return db.create_test(con, body.label, body.start_ts, body.end_ts)


@app.delete("/api/tests/{test_id}")
def delete_test(test_id: int, con=Depends(get_db)):
    db.delete_test(con, test_id)
    return {"ok": True}


@app.get("/api/tests/combined")
def combined(con=Depends(get_db)):
    return db.combined_ranking(con)


@app.get("/api/tests/{test_id}/correlation")
def test_correlation(test_id: int, con=Depends(get_db)):
    t = db.get_test(con, test_id)
    if not t:
        raise HTTPException(404, "test not found")
    end = t["end_ts"] or db.now_ts()  # rank a still-running test up to now
    return {
        "test": t,
        "end_used": end,
        "ranking": db.correlation(con, t["start_ts"], end),
    }


# --- static SPA (mounted last so /api/* routes take precedence) --------------
if os.path.isdir(STATIC_DIR):
    app.mount("/", StaticFiles(directory=STATIC_DIR, html=True), name="spa")
