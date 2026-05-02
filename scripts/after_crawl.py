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
        token = url.split("/")[-1] if url and "/" in url else "QaNGHZZU"
        url_req = f"https://api.divar.ir/v8/postcontact/web/contact_info_v2/{token}"
        headers = {
            "accept": "application/json, text/plain, */*",
            "accept-language": "en-US,en;q=0.9",
            "cache-control": "no-cache",
            "content-type": "application/json",
            "origin": "https://divar.ir",
            "pragma": "no-cache",
            "priority": "u=1, i",
            "referer": "https://divar.ir/",
            "sec-ch-ua": '"Google Chrome";v="147", "Not.A/Brand";v="8", "Chromium";v="147"',
            "sec-ch-ua-mobile": "?0",
            "sec-ch-ua-platform": '"Linux"',
            "sec-fetch-dest": "empty",
            "sec-fetch-mode": "cors",
            "sec-fetch-site": "same-site",
            "traceparent": "00-e2414084b63773c9c1fb89d51df5b6de-19f80d75cf09251c-00",
            "user-agent": "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.0.0 Safari/537.36",
            "x-render-type": "CSR",
            "x-screen-size": "664x813",
            "Cookie": "did=273c05e8-da8e-40f0-bcfd-87a0c054f7ec; cdid=d9e47f9d-9d7a-4d93-bcbb-89df45c4dc2a; _ga=GA1.1.1901794337.1751288309; theme=dark; city=isfahan; player_id=4f898f95-95db-437d-880e-2efed2fadc45; multi-city=iran%7C4%2C1; _ga_1G1K17N77F=GS2.1.s1764418683$o10$g1$t1764418683$j60$l0$h0; _ga_CCSRPLKB4B=GS2.1.s1777474359$o1$g1$t1777474436$j51$l0$h0; token=; ff=%7B%22f%22%3A%7B%22foreigner_payment_enabled%22%3Atrue%2C%22enable_filter_post_count_web%22%3Atrue%2C%22enable-places-selector-online-search-web%22%3Atrue%2C%22chat_message_disabled%22%3Atrue%2C%22web_sentry_disabled%22%3Atrue%2C%22web_sentry_sample_rate%22%3A0.1%2C%22enable-screen-size-metric%22%3Atrue%7D%2C%22e%22%3A1777738015860%2C%22r%22%3A1777820815860%7D; referrer=; sAccessToken=eyJraWQiOiJkLTE3NzcyMDE4Mzg2MjQiLCJ0eXAiOiJKV1QiLCJ2ZXJzaW9uIjoiNCIsImFsZyI6IlJTMjU2In0.eyJpYXQiOjE3Nzc3MzUxNTAsImV4cCI6MTc3NzczODc1MCwic3ViIjoiZTgxYThmOTUtNTY0ZS00MTRkLTlmZmItYjU2M2U0MmRjYTE3IiwidElkIjoicHVibGljIiwic2Vzc2lvbkhhbmRsZSI6IjNlNzk2YmE4LTM0NTMtNGU3Yy05MDcwLTk3MzNjZjA3YTQ5ZiIsInJlZnJlc2hUb2tlbkhhc2gxIjoiNmEyN2M0MDAwZDBjOTA0YWIxMjhlNmI2NzFlNGI4YjI3ZTlkOTcwN2M0MTBmMDYwZGIyNDFiMDg3M2Q3ZmQzNiIsInBhcmVudFJlZnJlc2hUb2tlbkhhc2gxIjpudWxsLCJhbnRpQ3NyZlRva2VuIjpudWxsLCJpc3MiOiJodHRwczovL2FwaS5kaXZhci5pci92OC9hdXRoZW50aWNhdGUiLCJwaG9uZU51bWJlciI6Iis5ODkyMjYyMDQ2ODEiLCJzdC1wZXJtIjp7InQiOjE3Nzc3MzUxNTAyMTgsInYiOltdfSwic3Qtcm9sZSI6eyJ0IjoxNzc3NzM1MTUwMjE4LCJ2IjpbXX19.TYu5UsK68Wt_k1vUxENn5_7ym8SahXty2I9HP4zt6DSDQe7cQ2GHFJjet2VweFcLAC-5S6uY4ZBRSV2cWFtwC7Xdp5V3NWEVh4uijoV79zOE18cpxE9wkrCt7cMML1LsGttHotc02_dl5l1iTPqvjeuqQFuIUZnfWt_-_ufPfgNe9fDMa7ggIHgQVYVaNEiEKGlH77FeWhvpNcqpN5QgUFpE1Ka1CI6QxcNg7HE0IHfiOwOFtjdRQNcRV7iAmrNAAQxdWDXFjLFzMQXZqoMjss9_TzE3aDdhh-zelj2A9VzWu0HEil4LaRa8101lIeJsJfQhZhAIkPxJO7iRJaHUjA; sFrontToken=eyJ1aWQiOiJlODFhOGY5NS01NjRlLTQxNGQtOWZmYi1iNTYzZTQyZGNhMTciLCJhdGUiOjE3Nzc3Mzg3NTAwMDAsInVwIjp7ImFudGlDc3JmVG9rZW4iOm51bGwsImV4cCI6MTc3NzczODc1MCwiaWF0IjoxNzc3NzM1MTUwLCJpc3MiOiJodHRwczovL2FwaS5kaXZhci5pci92OC9hdXRoZW50aWNhdGUiLCJwYXJlbnRSZWZyZXNoVG9rZW5IYXNoMSI6bnVsbCwicGhvbmVOdW1iZXIiOiIrOTg5MjI2MjA0NjgxIiwicmVmcmVzaFRva2VuSGFzaDEiOiI2YTI3YzQwMDBkMGM5MDRhYjEyOGU2YjY3MWU0YjhiMjdlOWQ5NzA3YzQxMGYwNjBkYjI0MWIwODczZDdmZDM2Iiwic2Vzc2lvbkhhbmRsZSI6IjNlNzk2YmE4LTM0NTMtNGU3Yy05MDcwLTk3MzNjZjA3YTQ5ZiIsInN0LXBlcm0iOnsidCI6MTc3NzczNTE1MDIxOCwidiI6W119LCJzdC1yb2xlIjp7InQiOjE3Nzc3MzUxNTAyMTgsInYiOltdfSwic3ViIjoiZTgxYThmOTUtNTY0ZS00MTRkLTlmZmItYjU2M2U0MmRjYTE3IiwidElkIjoicHVibGljIn19; csid=130d3f45a055ed2f4c"
        }
        payload = {"contact_uuid": uuid}
        try:
            resp = requests.post(url_req, json=payload, headers=headers, timeout=10)
            # return resp.text
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
