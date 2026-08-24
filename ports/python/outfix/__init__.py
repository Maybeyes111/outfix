"""outfix — clean malformed/polluted LLM output.

Port of github.com/Maybeyes111/outfix (Go). See the repo README for status
and limitations: heuristics-based, still evolving, report issues upstream.
"""

from .core import (
    Options,
    OutfixRepairFailed,
    RepairAction,
    Result,
    Session,
    TurnRecord,
    fix,
    process,
)

__all__ = ["Options", "OutfixRepairFailed", "RepairAction", "Result",
           "Session", "TurnRecord", "fix", "process"]

__version__ = "0.3.0"
