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
import re
import requests
import sys
import os


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

    # ── Extract contactUUID and fetch phone number ──────────────────────────────────────────────
    # Search for contactUUID in the page body JSON
    match = re.search(r'"contactUUID"\s*:\s*"([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})"', body)
    if not match:
        # fallback to contact_uuid key
        match = re.search(r'"contact_uuid"\s*:\s*"([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})"', body)
    if match:
        uuid = match.group(1)
        # Prepare request
        url_req = "https://api.divar.ir/v8/postcontact/web/contact_info_v2/QaWXEpOM"
        headers = {
            "accept": "application/json, text/plain, */*",
            "authorization": "Basic eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzaWQiOiJjZjJkOGM2Yi1hOGVkLTQ0ODAtODVhNi05ZjNhMGY3YTExNWQiLCJ1aWQiOiJlODFhOGY5NS01NjRlLTQxNGQtOWZmYi1iNTYzZTQyZGNhMTciLCJ1c2VyIjoiMDkyMjYyMDQ2ODEiLCJ2ZXJpZmllZF90aW1lIjoxNzc2MTQ0NzExLCJpc3MiOiJhdXRoIiwidXNlci10eXBlIjoicGVyc29uYWwiLCJ1c2VyLXR5cGUtZmEiOiLZvtmG2YQg2LTYrti124wiLCJleHAiOjE3Nzg3MzY3MTEsImlhdCI6MTc3NjE0NDcxMX0.nb5kOqzsxssJ9HOjTSXygEkHSY7Gw9GDPCYAG0C-pGc",
            "content-type": "application/json",
            "origin": "https://divar.ir",
            "referer": "https://divar.ir/",
            "user-agent": "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36",
            "Cookie": "did=97f82733-7253-4b7a-b32c-f90b1032b89f; cdid=f20cd141-9155-4700-b089-bc780620d686; theme=dark; multi-city=tehran%7C; city=tehran; token=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzaWQiOiJjZjJkOGM2Yi1hOGVkLTQ0ODAtODVhNi05ZjNhMGY3YTExNWQiLCJ1aWQiOiJlODFhOGY5NS01NjRlLTQxNGQtOWZmYi1iNTYzZTQyZGNhMTciLCJ1c2VyIjoiMDkyMjYyMDQ2ODEiLCJ2ZXJpZmllZF90aW1lIjoxNzc2MTQ0NzExLCJpc3MiOiJhdXRoIiwidXNlci10eXBlIjoicGVyc29uYWwiLCJ1c2VyLXR5cGUtZmEiOiLZvtmG2YQg2LTYrti124wiLCJleHAiOjE3Nzg3MzY3MTEsImlhdCI6MTc3NjE0NDcxMX0.nb5kOqzsxssJ9HOjTSXygEkHSY7Gw9GDPCYAG0C-pGc; _vid_t=+fLvdo/ZeNfBEWPerm81epm+nk95x+biMhudHe9D6BJ2VVGjWrk8LhA/+KjbM/LD91EeJP6IBIRbHg==; player_id=17641e75-c408-4cf7-bc5a-11210213757b; csid=bf05ca03560497b87a; ff=%7B%22f%22%3A%7B%22foreigner_payment_enabled%22%3Atrue%2C%22enable_filter_post_count_web%22%3Atrue%2C%22enable_non_lazy_image_post_card%22%3Atrue%2C%22device_fp_enable%22%3Atrue%2C%22enable-places-selector-online-search-web%22%3Atrue%2C%22chat_message_disabled%22%3Atrue%2C%22web_sentry_traces_sample_rate%22%3A0.001%2C%22enable-screen-size-metric%22%3Atrue%7D%2C%22e%22%3A1777634647366%2C%22r%22%3A1777717447366%7D; referrer=https%3A%2F%2Fdivar.ir%2Fs%2Ftehran"
        }
        payload = {"contact_uuid": uuid}
        try:
            resp = requests.post(url_req, json=payload, headers=headers, timeout=10)
            data = resp.json()
            # Navigate to phone number
            phone = None
            for widget in data.get("widget_list", []):
                action = widget.get("data", {}).get("action", {})
                payload = action.get("payload", {})
                phone = payload.get("phone_number")
                if phone:
                    break
            if phone:
                return phone
        except Exception as e:
            return f"ERROR fetching phone: {e}"
    # Fallback info
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
        # Write the output string to a file in the same directory as this script.
        out_path = os.path.join(os.path.dirname(__file__), "after_crawl_output.txt")
        try:
            with open(out_path, "a") as f:
                f.write(result + "\n")
        except Exception as e:
            print(f"ERROR: failed to write output to {out_path}: {e}", file=sys.stderr)


if __name__ == "__main__":
    main()
