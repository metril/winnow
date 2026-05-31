"""Request body models. Responses are plain dicts straight from the data layer."""

from typing import Optional

from pydantic import BaseModel


class MeterUpdate(BaseModel):
    label: Optional[str] = None
    notes: Optional[str] = None
    is_candidate: Optional[int] = None
    is_mine: Optional[int] = None


class TestStart(BaseModel):
    label: str = "load test"


class TestCreate(BaseModel):
    label: str = "load test"
    start_ts: str
    end_ts: str
