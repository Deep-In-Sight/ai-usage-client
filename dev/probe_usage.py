#!/usr/bin/python3
"""Debug probe: call the usage endpoint once and print status, rate-limit
related headers and the limits it reports.

Uses the Claude Code OAuth access token from ~/.claude/.credentials.json,
exactly like the extension does. The token is never printed. Every run is one
request against the endpoint's rate limit, so don't loop it.
"""
import json
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

CREDS = Path.home() / ".claude" / ".credentials.json"
URL = "https://api.anthropic.com/api/oauth/usage"


def show_headers(headers) -> None:
    for name, value in headers.items():
        lowered = name.lower()
        if any(word in lowered for word in ("retry", "limit", "reset", "date")):
            print(f"  {name}: {value}")


def main() -> int:
    oauth = json.loads(CREDS.read_text())["claudeAiOauth"]
    print(f"token expires in {(oauth['expiresAt'] / 1000 - time.time()) / 60:.0f} min")
    request = urllib.request.Request(URL, headers={
        "Authorization": f"Bearer {oauth['accessToken']}",
        "anthropic-beta": "oauth-2025-04-20",
        "User-Agent": "claude-usage-indicator/2.0",
        "Accept": "application/json",
    })
    try:
        with urllib.request.urlopen(request, timeout=20) as response:
            print(f"HTTP {response.status}")
            show_headers(response.headers)
            for item in json.loads(response.read().decode()).get("limits") or []:
                model = ((item.get("scope") or {}).get("model") or {}).get("display_name")
                print(f"  {item.get('kind')} {model or ''}: {item.get('percent')}% "
                      f"({item.get('severity')})")
    except urllib.error.HTTPError as e:
        print(f"HTTP {e.code}")
        show_headers(e.headers)
        print(f"  body: {e.read().decode()[:300]}")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
