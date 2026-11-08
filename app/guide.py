"""Where each view's explanation continues: the lesson of the public Guide.

WHY A VIEW LINKS OUT AT ALL. A "How to read this" block has a paragraph to
explain a chart, and some of these charts take a lesson. The Guide at
gammagrid.io is that lesson — the same author, the same words for the same
things — so the paragraph ends where the lesson begins rather than trying to
be one.

THE SAME LESSON THE HOSTED PRODUCT LINKS. Each view points at the one lesson
the hosted version's view points at, numbered the way the Guide numbers it, so
"lesson 7" means the same page whichever of the two somebody is reading.

PLAIN LINKS AND NOTHING ELSE. No tracking parameters, no click counting, no
redirect through anything of ours: this product reports nothing back about how
it is used (CONTRIBUTING.md), and a link that tells a server which installation
it came from is a report back. Following one is an ordinary visit to a public
page, and only when somebody chooses to make it.
"""

from __future__ import annotations

GUIDE_ORIGIN = "https://gammagrid.io"

# View (the label on the switcher) → (lesson slug, lesson number, lesson title).
# Every view has one; a check in tests/render_views.py holds each view's first
# explanation to carrying it.
VIEW_LESSONS = {
    "工作台": ("gex-heatmap", 7, "How to read a GEX heatmap"),
    "波动率": ("implied-volatility", 3, "Implied volatility, skew and the expected move"),
    "期权链": ("options-chain", 1, "The options chain"),
    "资金流": ("unusual-options-activity", 9, "Unusual options activity"),
    # Legacy names kept for backward compatibility
    "Overview": ("put-call-ratio", 2, "The put/call ratio"),
    "Changes": ("daily-routine", 11, "Reading positioning day to day"),
    "Max Pain / GEX": ("max-pain", 4, "Max pain"),
    "GEX Heatmap": ("gex-heatmap", 7, "How to read a GEX heatmap"),
    "Volatility (IV)": ("implied-volatility", 3, "Implied volatility, skew and the expected move"),
    "Contract": ("option-greeks-and-price", 10, "Option greeks: where the price went"),
    "Screener": ("options-chain", 1, "The options chain"),
    "Unusual Activity": ("unusual-options-activity", 9, "Unusual options activity"),
    "OI Delta": ("open-interest-changes", 8, "Open interest changes day to day"),
}


def url(slug: str | None = None) -> str:
    """A lesson's address, or the Guide's contents page without one."""
    return f"{GUIDE_ORIGIN}/guide/{slug}/" if slug else f"{GUIDE_ORIGIN}/guide/"


def link(view: str) -> str:
    """The "Guide · lesson N →" line that sits on a view's FIRST explanation.

    It shows on the explanation's label, where it is seen while the block is
    closed — and closed is how these blocks are nearly always seen. Only the
    first one per view carries it: the same link repeated down a page is
    noise, not help.

    DRAWN AS ITS OWN ELEMENT, NOT AS PART OF THE LABEL. Streamlit's expander
    cancels the default action of every click on its label so that it can
    toggle itself, and a link written into the label therefore opens the block
    and never the lesson. This is placed just above the block and drawn over
    the right end of its label (theme.py), outside the label's click handling.
    """
    slug, number, lesson = VIEW_LESSONS[view]
    tip = f"Lesson {number} of the GammaGrid Guide: {lesson}"
    return (
        f'<div class="gg-guide-link"><a href="{url(slug)}" target="_blank" '
        f'rel="noopener noreferrer" title="{tip}">Guide · lesson {number} '
        '<span aria-hidden="true">→</span></a></div>'
    )
