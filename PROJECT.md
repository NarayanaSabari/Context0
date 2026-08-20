# Kora — Memory for AI Agents

## What is Kora?

Kora is a **memory engine for AI agents**. It gives AI systems the ability to remember — just like how humans remember facts, experiences, and learned skills across conversations and over time.

Today, every time you talk to an AI assistant, it starts from scratch. It doesn't remember what you discussed yesterday, what decisions were made last week, or what your preferences are. Kora fixes this by providing a persistent, intelligent memory layer that any AI agent can plug into.

**Think of it as the brain's memory system, but for AI.**

---

## The Problem

### AI agents are amnesiac

Every AI agent today — whether it's a coding assistant, customer support bot, or personal AI — has the same fundamental problem: **it forgets everything between sessions.**

- You explain your project architecture to your AI coding assistant on Monday. On Tuesday, it has no idea what you told it.
- A customer support AI asks the same onboarding questions every single time a returning customer reaches out.
- A team uses multiple AI tools on the same project, but none of them share what they've learned — each one re-discovers the same things independently.

This isn't just annoying — it makes AI agents fundamentally less useful. They can never truly learn about you, your project, or your organization.

### Why existing solutions fall short

- **ChatGPT/Claude memory**: locked to one platform, can't be shared across tools
- **Vector databases**: find "similar" text, but don't understand relationships between memories
- **Custom solutions**: every team builds their own, poorly, from scratch

---

## What Kora Does

Kora provides three core capabilities:

### 1. Remember Everything

Feed Kora any conversation, document, or piece of information. It automatically extracts the important facts, preferences, and events — and stores them in a structured memory graph.

> **Example**: An AI coding assistant has a conversation where the user says "We switched from MySQL to PostgreSQL last week because we needed graph support." Kora automatically extracts:
> - Fact: The project uses PostgreSQL
> - Event: Migrated from MySQL to PostgreSQL
> - Reason: Needed graph support
> - Supersedes: The old fact "uses MySQL" is marked as outdated

### 2. Recall What Matters

When an AI agent needs context, Kora returns exactly the right memories — not 50 vaguely related chunks, but the 3-5 memories that actually matter for the current question. It understands relationships between memories, not just text similarity.

> **Example**: Agent asks "What database does this project use?"
> Kora returns: "PostgreSQL" (current fact) with context that it superseded MySQL, and the reason for the switch.

### 3. Build User Profiles

Kora automatically builds and maintains a profile for each user or project — combining stable facts (role, expertise, preferences) with recent context (current project, recent decisions). This profile makes every AI interaction personalized from the first message.

> **Example**: A user profile might contain:
> - **Static**: Senior engineer, prefers Go, uses Vim, values concise responses
> - **Dynamic**: Currently working on auth service, deployed to staging yesterday

---

## Use Cases

### For AI-Powered Products

| Use Case | How Kora Helps |
|----------|-------------------|
| **AI Coding Assistants** | Remember project architecture, tech stack, coding preferences, past decisions across sessions |
| **Customer Support Bots** | Know returning customers instantly — their plan, past issues, preferences — without asking again |
| **AI Tutoring Platforms** | Track what the student knows, where they struggle, their learning pace, and adapt accordingly |
| **Personal AI Assistants** | Remember your schedule patterns, communication preferences, important contacts, ongoing tasks |
| **Healthcare AI** | Maintain patient context — medical history, preferences, ongoing treatments — across appointments |
| **Legal AI** | Remember case details, client preferences, precedent decisions across document reviews |

### For Development Teams

| Use Case | How Kora Helps |
|----------|-------------------|
| **Multi-Agent Workflows** | Multiple AI agents working on the same project share a single memory — no duplicate work |
| **Tool Switching** | Switch from one AI tool to another without losing all accumulated context |
| **Team Knowledge Base** | AI agents learn from the entire team's interactions, building shared organizational memory |

---

## Why Kora is Different

| | Traditional Approach | Kora |
|---|---------------------|----------|
| **Memory structure** | Flat text chunks | Connected knowledge graph (facts linked to reasons, decisions linked to outcomes) |
| **Retrieval** | "Find similar text" | "Find what's related" — follows relationships, not just similarity |
| **Updates** | Old info mixed with new | Automatic contradiction detection — outdated facts are superseded, not duplicated |
| **Learning** | Starts fresh every session | Gets smarter over time — consolidates, promotes important patterns, forgets noise |
| **Sharing** | Locked to one tool | Any AI agent can plug in — framework-agnostic |
| **Privacy** | Your data on someone else's servers | Self-hosted — runs on your own infrastructure, your data never leaves |

---

## How It Works (Simple Version)

```
1. AI agent has a conversation with a user
                    ↓
2. Conversation is sent to Kora
                    ↓
3. Kora extracts facts, preferences, events
                    ↓
4. Memories are stored in a knowledge graph
   (with relationships: "this supersedes that",
    "this was caused by that", etc.)
                    ↓
5. Next time any AI agent needs context,
   it queries Kora and gets back
   exactly the right memories
```

---

## Who Is This For?

- **AI product builders** who want their product to remember users across sessions
- **Development teams** using AI coding assistants who are tired of repeating themselves
- **Enterprises** that need AI memory but can't send data to third-party cloud services
- **Anyone building with AI agents** who wants them to actually learn and improve over time

---

## Open Source

Kora is **100% open source** under the Apache 2.0 license. Every component — the engine, the database, the search, the UI — uses OSI-approved open-source software. No vendor lock-in, no proprietary dependencies.

You can run it on your own servers, in your own cloud, with your own data. Forever.
