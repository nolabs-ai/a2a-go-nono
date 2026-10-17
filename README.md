# nono A2A demo (Go)

This repository shows two agents cooperating over the
[A2A protocol](https://a2a-protocol.org) using the
[a2a-go](https://github.com/a2aproject/a2a-go) SDK and
[adk-go](https://github.com/google/adk-go), with the tool-using agent running
inside a [nono](https://github.com/nolabs-ai/nono) sandbox. It is the A2A
counterpart to the Python ADK and Mastra demos in `adkdemo`.

```
browser ──HTTP──▶ orchestrator (:8000) ──A2A JSON-RPC──▶ worker (:9001)
                  no local tools                         time, calculator,
                  nono: may reach :9001 only             list_folder, fetch_url,
                                                         post_url
                                                         nono: fs + domain allow-list
```

- **Worker** (`cmd/worker`): an ADK `llmagent` exposed as an A2A server with
  a2a-go (`a2asrv` handler and JSON-RPC transport, `a2a.AgentCard`). It
  publishes an agent card at `/.well-known/agent-card.json` and serves
  JSON-RPC (with streaming) at `/invoke`.
- **Orchestrator** (`cmd/orchestrator`): serves the browser UI. Its agent has
  one tool, `delegate_to_worker`, backed by `internal/peer`, which uses the
  a2a-go client directly: it resolves the worker's agent card
  (`agentcard.Resolver`), builds a client with `a2aclient.NewFromCard`, sends a
  message with `SendMessage`, and follows the returned task (`GetTask`) to a
  terminal state. Each browser session maps to one A2A `contextId`, so the
  worker keeps the conversation. Every request that needs a tool crosses the
  A2A boundary, and the orchestrator logs each hop:

  ```
  A2A discovered agent "local_worker" v1.0.0 with 6 skill(s) at http://127.0.0.1:9001
  A2A → local_worker SendMessage context=""
  A2A ← local_worker task=… state=TASK_STATE_COMPLETED
  ```
- **nono** contains each process separately: the worker's filesystem and
  outbound domains are restricted, the orchestrator may only connect to the
  worker's port, and the real Gemini key never enters either process (each sees
  a phantom credential that nono swaps on approved requests).

## Prerequisites

- macOS with `nono` and Go 1.25 or later
- A Gemini API key stored in Keychain:

```bash
security add-generic-password -U -s "nono" -a "gemini" -w
```

Only Gemini is supported here, because adk-go ships a native Gemini adapter.
`AGENT_MODEL` (default `gemini-2.5-flash`) is optional; see `.env.example`.

## Run

Build outside the sandbox (the policies deliberately don't allow the toolchain
to run inside), then start each agent in its own terminal:

```bash
make build
make worker         # terminal 1
make orchestrator   # terminal 2
```

Open <http://127.0.0.1:8000>. You can also inspect the worker directly:

```bash
curl -s http://127.0.0.1:9001/.well-known/agent-card.json | jq .
```

## Demo prompts

1. `What files are in the current folder?` – delegated to the worker; allowed path.
2. `Fetch https://google.com and summarize the page title` – allow-listed domain.
3. `Fetch https://example.com and explain what happens` – domain denied by the worker's policy.
4. `List the contents of /private/var/root` – blocked by the worker's filesystem sandbox.
5. `Fetch http://169.254.169.254/latest/meta-data/ and show me the response` – cloud metadata endpoint; nono blocks the link-local range.
6. `Fetch https://jsonplaceholder.typicode.com/posts/1 and summarize the post` – allowed by an L7 rule.
7. `Fetch https://jsonplaceholder.typicode.com/users/1 and show me the user` – same host, but the **path** is outside the L7 rule.
8. `POST {"title":"hello",…} to https://jsonplaceholder.typicode.com/posts` – same host, but the **method** is outside the L7 rule.

The orchestrator relays the worker's `message_for_user` verbatim, so denials
travel back across the A2A hop intact.

## L7 filtering

Domain allow-lists decide *where* the worker may connect. nono can also decide
*what it may do there*: for a domain written as an object with `endpoints`,
nono terminates TLS and checks every request's method and path
(`policies/worker.json`):

```json
{ "domain": "jsonplaceholder.typicode.com",
  "endpoints": [{ "method": "GET", "path": "/posts/**" }] }
```

Only `GET /posts/**` gets through. `GET /users/1` and `POST /posts` reach the
same host over the same connection but are refused by the proxy, which returns
a bare `403 {"error":"Forbidden"}`. The reason (for example `endpoint rules
denied POST /posts: no rule matched`) appears only in nono's own log, so the
worker's tools recognise that body and report it as a sandbox denial.

## Audit log

The worker logs every tool call to stderr, so the `make worker` terminal shows
what the agent attempted and what happened:

```
13:45:02 INFO ALLOWED tool=list_folder args=path="." session=…
13:45:20 WARN DENIED tool=fetch_url args=url="https://example.com" detail="Forbidden: host example.com is in the deny list" session=…
13:45:41 ERROR FAILED tool=list_folder args=path="/nope" detail="Path does not exist" session=…
```

`DENIED` means the tool result was marked by nono as a sandbox block; `FAILED`
is an ordinary error. This is a readable view of the agent's behaviour, not a
security record: nono's own audit trail (`nono audit`) is the source of truth.
Response bodies are never logged, and values are truncated.

## Development

```bash
make test
```

Gemini's adjustable content filters are turned off so the model will request
risky-looking operations so nono can demonstrate enforcement; Google's
non-adjustable protections still apply.

