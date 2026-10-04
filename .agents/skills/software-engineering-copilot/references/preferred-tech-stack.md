# Preferred Tech Stack

Use these as defaults, not absolute rules.

Prefer technologies I already know unless another option provides a meaningful advantage for the project's requirements.

If proposing a different technology, explain the benefit and tradeoff first.

## Backend

### General Backend / Distributed Systems

Preferred:

1. Go

Use this as the default for backend services, APIs, and distributed systems.

Prefer Encore.dev when its application framework, infrastructure abstractions, service discovery, deployment model, or local development workflow materially simplify the project.

### AI / ML / Data Science

Preferred:

1. Python
2. FastAPI

Use Python when the system depends substantially on:

- AI or LLM workloads
- machine learning
- data science
- Python-native libraries or ecosystems

Use FastAPI as the default API framework for Python services.

## Agentic Systems

Preferred:

- LangGraph
- LangSmith
- Python

Use LangGraph for agent workflows when the problem benefits from explicit state, branching, tool execution, or multi-step agent orchestration.

Use LangSmith for tracing, debugging, evaluation, and observability of agentic systems.

Do not introduce an agent framework when a simple LLM call or deterministic workflow is sufficient.

## Durable Workflows

Preferred:

- Temporal

Use Temporal when the system requires durable execution of long-running or multi-step workflows, especially when it needs:

- retries
- timers
- waiting for external events
- durable state
- recovery across process failures
- workflows lasting significantly longer than a normal request

Do not introduce Temporal for simple synchronous request-response flows.

## Database

Default:

- PostgreSQL

Prefer PostgreSQL unless the access patterns or system requirements strongly justify another database model.

## Cache

Default:

- No distributed cache

When caching is justified:

- Redis

Introduce Redis only when there is a concrete caching, coordination, rate-limiting, or ephemeral-state requirement.

## Infrastructure

Preferred:

- AWS ecosystem

When infrastructure is required, prefer an appropriate AWS-managed service before introducing an unrelated external platform.

Favor managed services where they reduce operational complexity without creating unacceptable cost or lock-in.

Choose specific AWS services based on the system requirements rather than treating any one service as the default.

## Web Frontend

Preferred:

- Next.js

Use Next.js as the default framework for web applications.

## Mobile

Preferred:

- React Native

Use React Native as the default for cross-platform mobile applications.

## General Principles

Prefer:

1. technologies already in this stack
2. simple architectures
3. managed infrastructure where appropriate
4. established libraries and frameworks
5. fewer technologies when one can reasonably serve multiple purposes

Avoid introducing additional databases, queues, caches, workflow engines, frameworks, or cloud providers unless the system requirements justify them.

When choosing between this preferred stack and an alternative, use:

**Preferred default:** ...
**Proposed alternative:** ...
**Reason to switch:** ...
**Tradeoff:** ...

If the benefit is marginal, use the preferred default.
