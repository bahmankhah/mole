#!/usr/bin/env python3
"""
Semantic embedding & FAISS search service for the crawler engine.

Called by Go as a subprocess. Supports three commands:
  1. embed   - Generate embedding(s) for text
  2. index   - Build/update a FAISS index from stored embeddings
  3. search  - Search the FAISS index for nearest neighbors

Usage:
    # Embed one or more texts (batch)
    echo '{"command":"embed","texts":["hello world","another text"],"model":"paraphrase-multilingual-MiniLM-L12-v2"}' | python3 semantic_embed.py

    # Build FAISS index from embedding files
    echo '{"command":"index","index_path":"/tmp/faiss.idx","embeddings_json":"/tmp/embeddings.json"}' | python3 semantic_embed.py

    # Search
    echo '{"command":"search","query":"find similar","model":"...","index_path":"/tmp/faiss.idx","top_k":10}' | python3 semantic_embed.py

Output (JSON to stdout):
    For embed:  {"embeddings": [[0.1, 0.2, ...], ...], "dimension": 384, "error": ""}
    For index:  {"total_indexed": 1234, "error": ""}
    For search: {"results": [{"id": 5, "score": 0.95}, ...], "error": ""}

Requirements:
    pip install sentence-transformers faiss-cpu numpy
"""

import json
import sys
import os
import struct
import numpy as np

# Global model cache to avoid reloading on repeated calls (not useful in subprocess mode,
# but helpful if the script is kept alive in the future)
_model_cache = {}


def get_model(model_name: str):
    """Load and cache a sentence-transformer model."""
    if model_name in _model_cache:
        return _model_cache[model_name]
    try:
        from sentence_transformers import SentenceTransformer
        model = SentenceTransformer(model_name)
        _model_cache[model_name] = model
        return model
    except ImportError:
        raise RuntimeError(
            "sentence-transformers is not installed. "
            "Run: pip install sentence-transformers faiss-cpu numpy"
        )


def cmd_embed(params: dict) -> dict:
    """Generate embeddings for a list of texts."""
    texts = params.get("texts", [])
    model_name = params.get("model", "paraphrase-multilingual-MiniLM-L12-v2")

    if not texts:
        return {"embeddings": [], "dimension": 0, "error": ""}

    try:
        model = get_model(model_name)
        embeddings = model.encode(texts, normalize_embeddings=True, show_progress_bar=False)
        # Convert to list of lists for JSON serialization
        result = {
            "embeddings": embeddings.tolist(),
            "dimension": int(embeddings.shape[1]),
            "error": "",
        }
        return result
    except Exception as e:
        return {"embeddings": [], "dimension": 0, "error": str(e)}


def cmd_index(params: dict) -> dict:
    """Build a FAISS index from a JSON file of embeddings.

    The embeddings_json file should contain a JSON array of objects:
      [{"id": <int>, "embedding": <base64 or raw bytes>}, ...]

    Or alternatively, 'embeddings_data' can be passed directly as a list of
    {"id": <int>, "vector": [float, ...]} dicts.
    """
    try:
        import faiss
    except ImportError:
        return {"total_indexed": 0, "error": "faiss-cpu is not installed. Run: pip install faiss-cpu"}

    index_path = params.get("index_path", "")
    embeddings_data = params.get("embeddings_data", [])

    if not embeddings_data:
        # Try loading from file
        data_path = params.get("embeddings_json", "")
        if data_path and os.path.exists(data_path):
            with open(data_path, "r") as f:
                embeddings_data = json.load(f)

    if not embeddings_data:
        return {"total_indexed": 0, "error": "no embeddings data provided"}

    try:
        ids = []
        vectors = []
        for item in embeddings_data:
            ids.append(int(item["id"]))
            vec = item["vector"]
            vectors.append(vec)

        vectors_np = np.array(vectors, dtype=np.float32)
        ids_np = np.array(ids, dtype=np.int64)
        dimension = vectors_np.shape[1]

        # Use IndexFlatIP (inner product = cosine similarity for normalized vectors)
        index = faiss.IndexIDMap(faiss.IndexFlatIP(dimension))
        index.add_with_ids(vectors_np, ids_np)

        # Save to disk
        if index_path:
            os.makedirs(os.path.dirname(index_path) or ".", exist_ok=True)
            faiss.write_index(index, index_path)

        return {"total_indexed": len(ids), "error": ""}
    except Exception as e:
        return {"total_indexed": 0, "error": str(e)}


def cmd_search(params: dict) -> dict:
    """Search the FAISS index for nearest neighbors of a query."""
    try:
        import faiss
    except ImportError:
        return {"results": [], "error": "faiss-cpu is not installed"}

    query_text = params.get("query", "")
    model_name = params.get("model", "paraphrase-multilingual-MiniLM-L12-v2")
    index_path = params.get("index_path", "")
    top_k = params.get("top_k", 10)

    if not query_text:
        return {"results": [], "error": "empty query"}
    if not index_path or not os.path.exists(index_path):
        return {"results": [], "error": f"index file not found: {index_path}"}

    try:
        # Load model and embed query
        model = get_model(model_name)
        query_embedding = model.encode([query_text], normalize_embeddings=True)
        query_np = np.array(query_embedding, dtype=np.float32)

        # Load FAISS index
        index = faiss.read_index(index_path)

        # Search
        k = min(top_k, index.ntotal)
        if k == 0:
            return {"results": [], "error": ""}

        scores, ids = index.search(query_np, k)

        results = []
        for i in range(len(ids[0])):
            page_id = int(ids[0][i])
            score = float(scores[0][i])
            if page_id >= 0:  # FAISS returns -1 for empty slots
                results.append({"id": page_id, "score": score})

        return {"results": results, "error": ""}
    except Exception as e:
        return {"results": [], "error": str(e)}


def main():
    """Read a JSON command from stdin, execute it, and write JSON to stdout."""
    try:
        raw = sys.stdin.read()
        params = json.loads(raw)
    except (json.JSONDecodeError, Exception) as e:
        json.dump({"error": f"invalid input: {e}"}, sys.stdout)
        sys.stdout.write("\n")
        sys.exit(1)

    command = params.get("command", "")

    if command == "embed":
        result = cmd_embed(params)
    elif command == "index":
        result = cmd_index(params)
    elif command == "search":
        result = cmd_search(params)
    else:
        result = {"error": f"unknown command: {command}"}

    json.dump(result, sys.stdout)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
