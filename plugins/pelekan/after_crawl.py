#!/usr/bin/env python3
"""
after_crawl.py — Pelekan plugin after-crawl hook for the Mole crawler.

Invoked after each successfully crawled page when this plugin is selected as
the after-crawl plugin.

Input (stdin): JSON with url, status_code, content_type, depth, body, job_id.
The body of a question-list page is the Peleyad GetQuestionList2 payload:

    {"status": 200, "data": {"totalRow": N, "dataList": [question, ...]}}

Each dataList item that can be a single-answer multiple-choice question is
appended to after_crawl_data/{job_id}.json. That file is the import document
described by Azmoon's docs/integration/pelekan-question-import.md.

Stdout is one short status line. Non-question pages exit 0 and change nothing.
"""

import fcntl
import html
import json
import logging
import os
import re
import sys
from html.parser import HTMLParser

PLUGIN_DIR = os.path.dirname(os.path.abspath(__file__))
DATA_DIR = os.path.join(PLUGIN_DIR, "after_crawl_data")
LOG_FILE = os.path.join(PLUGIN_DIR, "after_crawl.log")

TEXT_MAX = 500
# Azmoon shows custom option HTML only for these types (3, 4, or 5 choices).
# Type 2 is yes/no and does not display option1/option2 from these columns.
TYPE_BY_CHOICES = {3: 3, 4: 4, 5: 5}
IMAGE_TEXT = "سوال تصویری"


def _setup_logger() -> logging.Logger:
    log = logging.getLogger("pelekan_after_crawl")
    if log.handlers:
        return log
    log.setLevel(logging.INFO)
    handler = logging.FileHandler(LOG_FILE, encoding="utf-8")
    handler.setFormatter(logging.Formatter(
        "%(asctime)s [%(levelname)s] %(message)s",
        datefmt="%Y-%m-%d %H:%M:%S",
    ))
    log.addHandler(handler)
    return log


log = _setup_logger()


class _TextExtractor(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.parts = []

    def handle_starttag(self, tag, attrs):
        if tag in ("p", "br", "div", "li", "tr"):
            self.parts.append(" ")

    def handle_data(self, data):
        self.parts.append(data)


def plain_text(value) -> str:
    """Plain text for question.text. Tags are removed; character length is capped."""
    if not isinstance(value, str) or not value.strip():
        return ""
    parser = _TextExtractor()
    try:
        parser.feed(value)
        parser.close()
        text = "".join(parser.parts)
    except Exception:
        text = re.sub(r"<[^>]+>", " ", value)
    text = html.unescape(text)
    text = re.sub(r"\s+", " ", text).strip()
    return text[:TEXT_MAX]


def _filled(value) -> bool:
    return isinstance(value, str) and value.strip() != ""


def leading_choices(question: dict):
    """Return the leading choice1..choiceN run, or None when a later choice is filled after a gap."""
    slots = [question.get(f"choice{i}") for i in range(1, 6)]
    count = 0
    for slot in slots:
        if _filled(slot):
            count += 1
        else:
            break
    if any(_filled(slot) for slot in slots[count:]):
        return None
    return slots[:count]


def _answer_index(question: dict):
    key = question.get("key")
    if isinstance(key, bool) or key is None:
        return None
    if isinstance(key, int):
        return key
    if isinstance(key, str) and key.strip().isdigit():
        return int(key.strip())
    return None


def _stem_html(question: dict):
    body = question.get("questionbody")
    if _filled(body):
        return body.strip()
    image = question.get("questionImage")
    if _filled(image):
        src = html.escape(image.strip(), quote=True)
        return f'<p><img src="{src}" alt=""></p>'
    return ""


def to_azmoon_question(question: dict):
    """Map one Peleyad question to the Azmoon import object, or (None, reason)."""
    if not isinstance(question, dict):
        return None, "question is not an object"

    source_id = question.get("questionID")
    label = source_id if source_id is not None else "?"

    choices = leading_choices(question)
    if choices is None:
        return None, f"{label}: choices have a gap"
    qtype = TYPE_BY_CHOICES.get(len(choices))
    if qtype is None:
        return None, f"{label}: need 3, 4, or 5 leading choices, got {len(choices)}"

    answer = _answer_index(question)
    if answer is None or not 1 <= answer <= len(choices):
        return None, f"{label}: key {question.get('key')!r} is outside 1..{len(choices)}"

    stem = _stem_html(question)
    if not stem:
        return None, f"{label}: empty question body"

    text = plain_text(stem)
    if not text:
        text = IMAGE_TEXT

    item = {
        "sourceId": str(source_id) if source_id is not None else None,
        "type": qtype,
        "text": text,
        "html": stem,
        "answer": str(answer),
    }
    for index, choice in enumerate(choices, start=1):
        item[f"option{index}"] = choice.strip()

    explanation = question.get("answerbody")
    if _filled(explanation):
        item["fullAnswer"] = explanation.strip()
    return item, None


def _empty_doc(job_id: str) -> dict:
    return {"source": "pelekan", "jobId": job_id, "questions": [], "skipped": []}


def _load_doc(raw: str, job_id: str) -> dict:
    if not raw.strip():
        return _empty_doc(job_id)
    try:
        doc = json.loads(raw)
    except json.JSONDecodeError:
        return _empty_doc(job_id)
    if not isinstance(doc, dict) or not isinstance(doc.get("questions"), list):
        return _empty_doc(job_id)
    if not isinstance(doc.get("skipped"), list):
        doc["skipped"] = []
    doc["source"] = "pelekan"
    doc["jobId"] = job_id
    return doc


def merge_page(doc: dict, questions: list, skipped: list) -> tuple:
    """Append new questions. The first copy of a sourceId wins."""
    seen = {q.get("sourceId") for q in doc["questions"] if q.get("sourceId")}
    seen_skips = {s.get("sourceId") for s in doc["skipped"] if s.get("sourceId")}
    added = 0
    newly_skipped = 0
    for item in questions:
        source_id = item.get("sourceId")
        if source_id and source_id in seen:
            continue
        if source_id:
            seen.add(source_id)
        doc["questions"].append(item)
        added += 1
    for item in skipped:
        source_id = item.get("sourceId")
        if source_id and (source_id in seen or source_id in seen_skips):
            continue
        if source_id:
            seen_skips.add(source_id)
        doc["skipped"].append(item)
        newly_skipped += 1
    return added, newly_skipped


def write_doc(job_id: str, questions: list, skipped: list) -> str:
    os.makedirs(DATA_DIR, exist_ok=True)
    path = os.path.join(DATA_DIR, f"{job_id}.json")
    fd = os.open(path, os.O_RDWR | os.O_CREAT, 0o644)
    with os.fdopen(fd, "r+", encoding="utf-8") as handle:
        fcntl.flock(handle.fileno(), fcntl.LOCK_EX)
        try:
            doc = _load_doc(handle.read(), job_id)
            added, newly_skipped = merge_page(doc, questions, skipped)
            handle.seek(0)
            handle.truncate()
            json.dump(doc, handle, ensure_ascii=False, separators=(",", ":"))
        finally:
            fcntl.flock(handle.fileno(), fcntl.LOCK_UN)
    return f"added={added} skipped={newly_skipped} total={len(doc['questions'])} file={path}"


def questions_from_body(body: str):
    """Return (questions, skipped, status). status is 'ok' or 'ignore'."""
    try:
        payload = json.loads(body)
    except json.JSONDecodeError:
        return [], [], "ignore"
    if not isinstance(payload, dict):
        return [], [], "ignore"
    data = payload.get("data")
    if not isinstance(data, dict) or not isinstance(data.get("dataList"), list):
        return [], [], "ignore"

    questions = []
    skipped = []
    for raw in data["dataList"]:
        item, reason = to_azmoon_question(raw if isinstance(raw, dict) else {})
        if item:
            questions.append(item)
            continue
        source_id = raw.get("questionID") if isinstance(raw, dict) else None
        skipped.append({
            "sourceId": str(source_id) if source_id is not None else None,
            "reason": reason,
        })
    return questions, skipped, "ok"


def main() -> int:
    try:
        page = json.load(sys.stdin)
    except json.JSONDecodeError as exc:
        print(f"invalid stdin: {exc}", file=sys.stderr)
        return 1
    if not isinstance(page, dict):
        print("invalid stdin: expected an object", file=sys.stderr)
        return 1

    job_id = os.path.basename(str(page.get("job_id") or "").strip())
    url = page.get("url") or ""
    body = page.get("body") or ""
    if not job_id or job_id in (".", ".."):
        print("missing job_id", file=sys.stderr)
        return 1

    questions, skipped, status = questions_from_body(body)
    if status != "ok":
        log.info("ignore url=%s", url)
        print("not a question list")
        return 0

    try:
        summary = write_doc(job_id, questions, skipped)
    except Exception as exc:
        log.exception("write failed url=%s", url)
        print(f"write failed: {exc}", file=sys.stderr)
        return 1

    log.info("url=%s parsed=%d %s", url, len(questions) + len(skipped), summary)
    print(summary)
    return 0


if __name__ == "__main__":
    sys.exit(main())
