#!/usr/bin/env python3
"""Exercise the running program through its real management and proxy endpoints.

python3 scripts/smoke.py --admin http://localhost:8081 --proxy http://localhost:8080
Uses a temporary local origin and restores the original configuration on exit.
"""

import argparse
import concurrent.futures
import contextlib
import json
import threading
import time
import urllib.request
from collections import Counter
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


def call(url, *, data=None, headers=None, method=None):
    body = None if data is None else json.dumps(data).encode()
    request = urllib.request.Request(url, body, headers or {}, method=method)
    if body is not None:
        request.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(request, timeout=20) as response:
        return response.read(), response.headers


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--admin", default="http://localhost:8081")
    parser.add_argument("--proxy", default="http://localhost:8080")
    parser.add_argument("--origin-host", default="127.0.0.1", help="Origin hostname as seen by mycdn (host.docker.internal for Docker)")
    args = parser.parse_args()
    counts, lock = Counter(), threading.Lock()
    payload = b"mycdn shared origin\n" * 4096

    class Origin(BaseHTTPRequestHandler):
        def do_GET(self):
            with lock:
                counts[self.path] += 1
            self.send_response(200)
            self.send_header("Content-Type", "text/plain")
            self.send_header("Cache-Control", "public, max-age=300")
            self.send_header("ETag", '"smoke-content"')
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)

        def log_message(self, *_):
            pass

    origin = ThreadingHTTPServer(("0.0.0.0", 0), Origin)
    threading.Thread(target=origin.serve_forever, daemon=True).start()
    original, headers = call(args.admin + "/api/config")
    config = json.loads(original)
    # Huma's $schema link is response metadata, not user configuration.
    config.pop("$schema", None)
    saved = json.loads(json.dumps(config))
    site_id = f"smoke-{time.time_ns():x}"
    host = site_id + ".mycdn.local"
    site = {
        "id": site_id,
        "name": "Smoke origin",
        "enabled": True,
        "domains": [host],
        "origins": [{"id": "main", "name": "Local fixture", "url": f"http://{args.origin_host}:{origin.server_port}"}],
        "default_origin": "main",
    }
    config["sites"] = config.get("sites", []) + [site]
    save_headers = {"If-Match": headers["ETag"]} if headers.get("ETag") else {}
    started = datetime.now(timezone.utc)
    try:
        call(args.admin + "/api/config", data=config, headers=save_headers, method="PUT")

        def fetch(path):
            return call(args.proxy + path, headers={"Host": host})[0]

        assert fetch("/asset") == payload
        assert fetch("/asset") == payload
        assert counts["/asset"] == 1, f"Expected a cache hit, origin saw {counts['/asset']} requests"
        with concurrent.futures.ThreadPoolExecutor(max_workers=12) as workers:
            results = list(workers.map(lambda _: fetch("/shared"), range(24)))
        assert all(body == payload for body in results)
        assert counts["/shared"] == 1, f"Expected one acquisition, saw {counts['/shared']}"
        # Content-Length lets a client finish before the server's final EOF/read
        # and durable publication. Observe publication rather than sleep for it.
        deadline = time.monotonic() + 5
        while True:
            entries, _ = call(args.admin + f"/api/cache/entries?site_id={site_id}&limit=100")
            if len(json.loads(entries)["entries"]) >= 2:
                break
            assert time.monotonic() < deadline, "Completed cache fills did not publish"
            time.sleep(0.05)
        refreshed, _ = call(args.admin + "/api/cache/refresh", data={"urls": [f"http://{host}/asset"], "site_id": site_id})
        assert all(item["status"] == "refreshed" for item in json.loads(refreshed)["results"])
        assert counts["/asset"] == 2, "Refresh must perform an actual acquisition"
        purged, _ = call(args.admin + "/api/cache/purge", data={"site_id": site_id, "path": "/asset"})
        assert json.loads(purged)["purged"] >= 1
        assert fetch("/asset") == payload
        assert counts["/asset"] == 3

        if config["analytics"]["enabled"]:
            # The queue flushes in the background; assert the observable outcome,
            # rather than coupling the smoke to an internal Flush test seam.
            deadline = time.monotonic() + 10
            while True:
                query = {
                    "from": (started - timedelta(seconds=1)).isoformat(timespec="milliseconds"),
                    "to": datetime.now(timezone.utc).isoformat(timespec="milliseconds"),
                    "filters": {"site_ids": [site_id]},
                    "group_by": ["host", "cache_result"],
                }
                raw, _ = call(args.admin + "/api/analytics/query", data=query)
                summary = json.loads(raw)["summary"]
                if summary["requests"] >= 27:
                    assert summary["requests"] == 27, summary
                    assert summary["request_errors"] == 0, summary
                    assert summary["origin_attempts"] == 4, summary
                    assert summary["origin_errors"] == 0, summary
                    assert summary["response_bytes"] == 27 * len(payload), summary
                    break
                assert time.monotonic() < deadline, "Analytics did not ingest completed requests"
                time.sleep(0.2)
        print("PASS: config, real origin, cache reuse, shared acquisition, refresh, scoped purge, analytics")
    finally:
        with contextlib.suppress(Exception):
            call(args.admin + "/api/cache/purge", data={"site_id": site_id})
        with contextlib.suppress(Exception):
            _, current = call(args.admin + "/api/config")
            restore_headers = {"If-Match": current["ETag"]} if current.get("ETag") else {}
            call(args.admin + "/api/config", data=saved, headers=restore_headers, method="PUT")
        origin.shutdown()
        origin.server_close()


if __name__ == "__main__":
    main()
