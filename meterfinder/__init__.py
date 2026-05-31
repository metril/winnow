"""meterfinder — shared data layer for the capture ingester and the web API.

Pure stdlib (sqlite3 + json) so the slim capture container and the FastAPI app
can both import it. Extraction and consumption-delta math live here only, so the
two halves can never disagree about what a reading means.
"""

from . import db  # noqa: F401
