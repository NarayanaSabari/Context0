from __future__ import annotations

from datetime import date

from chaser.replies import find_promise_date, is_dispute


def test_finds_iso_date_in_reply() -> None:
    assert find_promise_date("We will pay invoice inv_017 by 2026-09-08.") == date(2026, 9, 8)


def test_no_date_returns_none() -> None:
    assert find_promise_date("Paying invoice inv_017 now, thank you.") is None


def test_dispute_keywords() -> None:
    assert is_dispute("Invoice inv_017 amount looks wrong, we are disputing it.")
    assert is_dispute("This is INCORRECT, please review.")


def test_non_dispute_text() -> None:
    assert not is_dispute("Paying invoice inv_017 now, thank you for the reminder.")
