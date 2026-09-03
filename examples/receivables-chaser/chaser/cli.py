"""Command-line entry point.

    python -m chaser run --recorded --days 21
    python -m chaser run --live
"""

from __future__ import annotations

import argparse
import json
import logging
import os
import sys
import uuid
from datetime import timedelta
from pathlib import Path
from typing import Optional

from . import agent, report
from .drafter import Drafter, LLMDrafter, TemplateDrafter
from .memory import KoraMemory, Memory, NullMemory, SafeMemory
from .razorpay import LiveRazorpay, RazorpayClient, RecordedRazorpay

EXAMPLE_ROOT = Path(__file__).resolve().parent.parent
DEFAULT_WORLD = EXAMPLE_ROOT / "fixtures" / "world.json"


def _build_memory(args: argparse.Namespace) -> Memory:
    url = args.kora_url or os.environ.get("KORA_URL")
    api_key = args.api_key or os.environ.get("KORA_API_KEY")
    if not url or not api_key:
        return NullMemory()
    return SafeMemory(KoraMemory(url, api_key, merchant=args.merchant))


def _build_drafter() -> Drafter:
    base_url = os.environ.get("OPENAI_BASE_URL")
    api_key = os.environ.get("OPENAI_API_KEY")
    model = os.environ.get("OPENAI_MODEL", "gpt-4o-mini")
    template = TemplateDrafter()
    if base_url and api_key:
        return LLMDrafter(base_url, api_key, model, fallback=template)
    return template


def _build_razorpay(args: argparse.Namespace) -> RazorpayClient:
    if args.live:
        key_id = os.environ.get("RAZORPAY_KEY_ID")
        key_secret = os.environ.get("RAZORPAY_KEY_SECRET")
        if not key_id or not key_secret:
            print(
                "error: --live needs RAZORPAY_KEY_ID and RAZORPAY_KEY_SECRET in the environment",
                file=sys.stderr,
            )
            raise SystemExit(2)
        return LiveRazorpay(key_id, key_secret)
    return RecordedRazorpay(args.world)


def _run(args: argparse.Namespace) -> int:
    logging.basicConfig(level=logging.WARNING, format="%(levelname)s %(name)s: %(message)s")

    razorpay = _build_razorpay(args)
    memory = _build_memory(args)
    drafter = _build_drafter()

    # A live run has no simulated days to tick through: it looks at the
    # invoices Razorpay has right now and acts once, the way a cron job
    # firing daily against the real API would.
    days = 1 if args.live else args.days

    # A recorded run gets its own memory namespace unless --resume is passed.
    #
    # Kora persists, so without this a second run reads the first run's
    # memories and behaves differently: the agent sees contacts it has not
    # made yet in this run and skips them. Two identical commands produced
    # Rs 221,200 and then Rs 259,900, which makes the demo unreproducible.
    #
    # The namespace has to be genuinely fresh rather than merely derived from
    # the run's inputs: a namespace that is a pure function of the command is
    # the same namespace on the second run, which is the accumulating case
    # again. So this is a random suffix, and the determinism it buys is of
    # the output, not of the name -- every run starts from an empty store and
    # therefore makes the same decisions.
    #
    # A --recorded run replays a whole simulated history from day zero, so
    # starting clean is what makes it a replay rather than a continuation. A
    # --live run is the opposite: it is one day's work against real invoices,
    # the way a daily cron fires, and it MUST read what previous days wrote or
    # the agent re-chases customers who already promised to pay. So live runs
    # always keep their history, and --resume exists to ask for the same
    # continuity from a recorded run.
    merchant = args.merchant
    if not args.resume and not args.live:
        merchant = f"{args.merchant}-{uuid.uuid4().hex[:8]}"

    result = agent.run(razorpay, memory, drafter, days=days, merchant=merchant)

    start_date = razorpay.today()
    end_date = start_date + timedelta(days=days - 1)
    data = report.build_report(
        result, args.merchant, start_date, end_date, memory_status=memory.status(),
    )

    out_dir = Path(args.out_dir)
    out_dir.mkdir(parents=True, exist_ok=True)
    with (out_dir / "audit.jsonl").open("w") as f:
        for entry in result.audit:
            f.write(json.dumps(entry.__dict__) + "\n")
    report.write_json(data, out_dir / "report.json")

    report.print_report(data)
    return 0


def main(argv: Optional[list[str]] = None) -> int:
    parser = argparse.ArgumentParser(
        prog="chaser", description="Deterministic receivables chaser backed by Kora memory.",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    run_parser = sub.add_parser("run", help="run the chase loop")
    mode = run_parser.add_mutually_exclusive_group()
    mode.add_argument("--recorded", action="store_true", help="replay the recorded fixture world (default)")
    mode.add_argument("--live", action="store_true", help="talk to Razorpay test-mode via RAZORPAY_KEY_ID/SECRET")
    run_parser.add_argument("--days", type=int, default=21, help="daily ticks to simulate in --recorded mode")
    run_parser.add_argument("--world", default=str(DEFAULT_WORLD), help="path to the recorded world fixture")
    run_parser.add_argument("--merchant", default="acme", help="merchant slug; scopes the Kora project")
    run_parser.add_argument(
        "--resume", action="store_true",
        help="carry memory over from previous runs instead of starting from an empty store",
    )
    run_parser.add_argument("--kora-url", default=None, help="overrides the KORA_URL environment variable")
    run_parser.add_argument("--api-key", default=None, help="overrides the KORA_API_KEY environment variable")
    run_parser.add_argument("--out-dir", default=str(EXAMPLE_ROOT), help="where to write audit.jsonl and report.json")
    run_parser.set_defaults(func=_run)

    args = parser.parse_args(argv)
    return args.func(args)
