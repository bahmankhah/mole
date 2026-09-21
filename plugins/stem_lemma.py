#!/usr/bin/env python3
"""
Stemming & lemmatization service for the Mole crawler.

Protocol (JSON over stdin/stdout, one request → one response):

Request:
    {
        "command": "process",          // "process" | "batch" | "ping"
        "text":    "some text …",      // for "process"
        "texts":   ["t1", "t2", …],    // for "batch"
        "lang":    "fa"                 // "fa" | "en" | "auto"
    }

Response:
    {
        "tokens":  ["stem1", "stem2", …],          // for "process"
        "results": [["stem1", …], ["stem1", …]],   // for "batch"
        "error":   ""
    }

Usage:
    echo '{"command":"process","text":"کتاب‌ها را خواندم","lang":"fa"}' | python3 stem_lemma.py
"""

from __future__ import annotations

import json
import re
import sys
import unicodedata
from typing import List, Optional

# ── Lazy-loaded NLP backends ────────────────────────────────────────────────

_hazm_normalizer = None
_hazm_stemmer = None
_hazm_lemmatizer = None
_snowball_stemmer = None


def _init_hazm():
    """Initialise Hazm components (Persian)."""
    global _hazm_normalizer, _hazm_stemmer, _hazm_lemmatizer
    if _hazm_normalizer is not None:
        return
    try:
        from hazm import Normalizer, Stemmer, Lemmatizer
        _hazm_normalizer = Normalizer()
        _hazm_stemmer = Stemmer()
        _hazm_lemmatizer = Lemmatizer()
    except ImportError:
        raise RuntimeError("hazm is not installed – run: pip install hazm")


def _init_snowball():
    """Initialise NLTK Snowball stemmer (English)."""
    global _snowball_stemmer
    if _snowball_stemmer is not None:
        return
    try:
        from nltk.stem import SnowballStemmer
        _snowball_stemmer = SnowballStemmer("english")
    except ImportError:
        raise RuntimeError("nltk is not installed – run: pip install nltk")


# ── Text normalisation ──────────────────────────────────────────────────────

# Matches any character that is NOT a Unicode letter or digit.
_NON_ALPHA = re.compile(r"[^\w\s]", re.UNICODE)
# Collapse whitespace.
_MULTI_WS = re.compile(r"\s+")


def _normalise(text: str, lang: str) -> str:
    """Lower-case, strip diacritics, collapse whitespace."""
    text = text.lower()
    if lang == "fa" and _hazm_normalizer is not None:
        text = _hazm_normalizer.normalize(text)
    # Replace punctuation with space so adjacent tokens split correctly.
    text = _NON_ALPHA.sub(" ", text)
    text = _MULTI_WS.sub(" ", text).strip()
    return text


# ── Core processing ─────────────────────────────────────────────────────────

def _detect_lang(text: str) -> str:
    """Very fast heuristic: if >30 % of non-space chars are in the Arabic
    Unicode block we call it Persian, otherwise English."""
    if not text:
        return "en"
    arabic_count = sum(1 for ch in text if "\u0600" <= ch <= "\u06ff" or "\ufb50" <= ch <= "\ufdff")
    total = max(sum(1 for ch in text if not ch.isspace()), 1)
    return "fa" if arabic_count / total > 0.3 else "en"


def process_text(text: str, lang: str) -> List[str]:
    """Stem / lemmatise *text* and return a list of processed tokens."""
    if not text or not text.strip():
        return []

    if lang == "auto":
        lang = _detect_lang(text)

    normalised = _normalise(text, lang)
    raw_tokens = normalised.split()

    if lang == "fa":
        _init_hazm()
        result = []
        for tok in raw_tokens:
            if len(tok) < 2:
                continue
            # Lemmatizer first (more accurate); fall back to stemmer.
            lemma = _hazm_lemmatizer.lemmatize(tok)
            if lemma and lemma != tok:
                # Hazm lemmatizer may return "word#verb" form – take the base.
                lemma = lemma.split("#")[0]
                result.append(lemma)
            else:
                stem = _hazm_stemmer.stem(tok)
                result.append(stem if stem else tok)
        return result

    else:  # English / default
        _init_snowball()
        result = []
        for tok in raw_tokens:
            if len(tok) < 2:
                continue
            stem = _snowball_stemmer.stem(tok)
            result.append(stem if stem else tok)
        return result


def process_batch(texts: List[str], lang: str) -> List[List[str]]:
    """Process multiple texts in one call."""
    return [process_text(t, lang) for t in texts]


# ── Main entry point ────────────────────────────────────────────────────────

def main():
    raw = sys.stdin.read().strip()
    if not raw:
        _respond(error="empty input")
        return

    try:
        req = json.loads(raw)
    except json.JSONDecodeError as e:
        _respond(error=f"invalid JSON: {e}")
        return

    command = req.get("command", "process")
    lang = req.get("lang", "fa")

    try:
        if command == "ping":
            _respond(tokens=[], error="")
            return

        if command == "process":
            text = req.get("text", "")
            tokens = process_text(text, lang)
            _respond(tokens=tokens)

        elif command == "batch":
            texts = req.get("texts", [])
            results = process_batch(texts, lang)
            _respond(results=results)

        else:
            _respond(error=f"unknown command: {command}")

    except Exception as e:
        _respond(error=str(e))


def _respond(**kwargs):
    resp = {"error": ""}
    resp.update(kwargs)
    sys.stdout.write(json.dumps(resp, ensure_ascii=False) + "\n")
    sys.stdout.flush()


if __name__ == "__main__":
    main()
