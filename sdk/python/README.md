# Kora Python SDK

The dependency-free Python client for the Kora memory engine.
It uses Kora's REST gateway to store, query, connect, and delete memories, inspect graph context, manage sessions, and check engine health.

## Install

Until the SDK is published to PyPI, download the wheel from the matching [Kora GitHub Release](https://github.com/NarayanaSabari/Kora/releases) and install it locally:

```bash
pip install ./kora-0.1.1-py3-none-any.whl
```

To work from a repository clone instead:

```bash
pip install ./sdk/python
```

## Use

```python
from kora import KoraClient

client = KoraClient(
    endpoint="localhost:50051",
    api_key="your-key",
    project="my-project",
)

client.store(
    "The project uses PostgreSQL 18.",
    type="semantic",
    tags=["database"],
)

for result in client.query("Which database does this project use?", top_k=3):
    print(result.score, result.memory.content)
```

Kora must already be running and reachable from the client.
See the [Kora documentation](https://kora.sabarinarayana.com/docs/) for installation, authentication, memory types, project scoping, and agent integration.
