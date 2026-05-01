#!/usr/bin/env python3
"""
after_crawl.py — Custom after-crawl hook for the Mole crawler.

Invoked by the crawler engine after each successfully crawled page when
"Run After-Crawl Script" is enabled in the job settings.

Input  (stdin): JSON object with the following fields:
    url          (str)  — the crawled page URL
    status_code  (int)  — HTTP response status code
    content_type (str)  — response Content-Type header
    depth        (int)  — crawl depth of this page
    body         (str)  — raw response body (HTML / text)
    job_id       (str)  — UUID of the crawl job

Output (stdout): any text; logged by the engine as [AfterCrawl] <url> => <output>.
Stderr and non-zero exit codes are logged as errors but do not abort the crawl.

Example use-cases:
    - Extract and export specific data to a file or external API
    - Detect patterns / keywords not supported by the phrase engine
    - Send alerts when particular content is found
    - Feed content into an external pipeline

Runs inside scripts/.venv — add dependencies to scripts/requirements.txt
and run scripts/setup_python.sh to install them.
"""

import json
import sys


def process(page: dict) -> str:
    """
    Process a single crawled page.

    Args:
        page: dict with keys url, status_code, content_type, depth, body, job_id

    Returns:
        A short string that will be logged by the engine (stdout).
        Return an empty string to produce no log output.
    """
    url = page.get("url", "")
    status = page.get("status_code", 0)
    depth = page.get("depth", 0)
    body = page.get("body", "")

    # ── Example: log basic info ──────────────────────────────────────────────
    return f"status={status} depth={depth} body_len={len(body)} url={url}"

    # ── Example: write to a file ─────────────────────────────────────────────
    # with open("/tmp/crawled_urls.txt", "a") as f:
    #     f.write(f"{url}\n")
    # return f"appended {url}"

    # ── Example: alert on keyword ────────────────────────────────────────────
    # if "confidential" in body.lower():
    #     return f"KEYWORD MATCH: 'confidential' found at {url}"
    # return ""


def main():
    raw = sys.stdin.read()
    if not raw.strip():
        sys.exit(0)

    try:
        page = json.loads(raw)
    except json.JSONDecodeError as e:
        print(f"ERROR: failed to parse input JSON: {e}", file=sys.stderr)
        sys.exit(1)

    result = process(page)
    if result:
        print(result)


if __name__ == "__main__":
    main()
