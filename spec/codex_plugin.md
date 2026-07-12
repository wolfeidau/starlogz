# Codex compound-knowledge plugin

## Context

Starlogz provides durable project memory through MCP, but agents still need a
repeatable policy for deciding when to recall, verify, and persist knowledge.
The existing personal `compound-knowledge` Codex skill has validated that
workflow during development of this repository.

The workflow should be available to other Starlogz users without requiring
them to recreate a local skill or copy instructions manually. It should also
be represented in the README as a concrete example of Starlogz supporting an
agent across multiple sessions.

## Goals

- Name OpenAI Codex as one of the agents used to develop Starlogz.
- Give users a repeatable recall-before-work and consolidate-after-work flow.
- Distribute the workflow through a supported Codex installation mechanism.
- Connect the workflow to a user-provided Starlogz MCP server.
- Keep repository files and current source authoritative over stored memory.
- Prevent secrets, raw logs, and low-value task noise from being persisted.

## Non-goals

- Bundle or operate a Starlogz server for the user.
- Provide a shared hosted Starlogz instance.
- Hard-code a Starlogz deployment URL or project slug.
- Replace `AGENTS.md`, repository documentation, or source inspection.
- Support non-Codex agent packaging in the first iteration.

## Distribution model

The workflow remains authored as a skill and is packaged as a plugin for
installation. The initial plugin lives in this repository so its workflow can
evolve with the Starlogz MCP API.

Proposed layout:

```text
.agents/
└── plugins/
    └── marketplace.json
plugins/
└── starlogz-codex/
    ├── .codex-plugin/
    │   └── plugin.json
    └── skills/
        └── compound-knowledge/
            ├── agents/
            │   └── openai.yaml
            └── SKILL.md
```

The plugin manifest identifies `starlogz-codex` and exposes the bundled
skills. The marketplace entry makes the plugin discoverable from this
repository. The skill's `agents/openai.yaml` declares the Starlogz MCP
dependency so Codex can guide the user through connecting and authenticating
their own server.

The intended installation path is:

```bash
codex plugin marketplace add wolfeidau/starlogz
codex plugin add starlogz-codex@starlogz
```

If the plugin develops an independent release cadence or adds integrations
beyond Starlogz, it can move to a dedicated repository without changing the
skill's behavior.

## Skill behavior

### Project selection

The skill derives the project slug from the current repository name unless the
user supplies one explicitly. If the derived slug is ambiguous, it lists
available projects and selects an existing match before creating anything.

The published skill must not contain the personal skill's special case that
always uses `starlogz` for this repository.

### Recall before work

For meaningful repository work, the skill:

1. Verifies Starlogz access when the connection state is unknown.
2. Searches for relevant conventions, decisions, preferences, architecture,
   testing, deployment, and feature-specific context.
3. Checks existing tags before creating new tags.
4. Verifies recalled information against current repository files.

Stored insights provide context, not authority. Explicit user instructions,
repository guidance, specifications, and current source take precedence.

### Persist durable knowledge

The skill writes only information that is reusable, specific, verified, and
minimal. Suitable content includes:

- stable user or team preferences;
- architecture and integration decisions;
- recurring commands and operational procedures;
- validated debugging findings and compatibility constraints;
- concise summaries of meaningful completed work.

The skill must not store credentials, access tokens, private keys, secrets,
raw command output, speculative findings, or step-by-step activity logs.

Keyed insights represent current authoritative values and are updated in
place. Decisions, discoveries, and historical incidents remain append-only.
Stale insights are corrected or superseded rather than silently reused.

### Failure behavior

If Starlogz is unavailable, the agent continues with repository-local context
and reports that recall or persistence could not be completed. A memory
failure must not block otherwise safe repository work.

## Documentation changes

The README should:

- name OpenAI Codex in the existing `Built with AI` section;
- explain that Starlogz is used during its own development as persistent agent
  memory;
- describe the recall, verify, and consolidate loop in user-focused terms;
- link to the plugin installation instructions;
- state that users connect the plugin to their own Starlogz deployment;
- distinguish the plugin workflow from the Starlogz MCP server itself.

Suggested summary:

> Starlogz is developed with OpenAI Codex and uses Starlogz itself as
> persistent agent memory. The optional `compound-knowledge` skill recalls
> relevant project insights before work, verifies them against the repository,
> and stores only durable decisions, preferences, and validated findings
> afterward.

## Examples required before release

The published skill documentation includes one concise example for each core
behavior:

- recalling architecture and workflow context before starting a change;
- updating a keyed preference such as a testing or commit convention;
- appending a decision with rationale that should remain in project history;
- continuing safely when the Starlogz MCP server is unavailable.

## Delivery plan

1. Generalize the existing personal `compound-knowledge` skill.
2. Scaffold the `starlogz-codex` plugin and repository marketplace entry.
3. Declare and test the Starlogz MCP dependency and OAuth setup flow.
4. Add examples and validate explicit and implicit skill triggering.
5. Update the README with the Codex workflow and installation instructions.
6. Test installation from a clean Codex environment against a non-local
   Starlogz deployment.
7. Publish the plugin with the next suitable Starlogz release.

## Acceptance criteria

- A new user can install the plugin from the repository marketplace.
- Codex discovers the skill from its metadata and can invoke it explicitly.
- The plugin guides the user to configure their own Starlogz MCP endpoint.
- Recall uses the current repository slug by default.
- Repository evidence wins when a stored insight is stale.
- The skill does not persist secrets or noisy one-off output.
- Unavailable Starlogz access degrades gracefully.
- The README clearly explains the value without presenting Codex as a runtime
  dependency of the Starlogz server.
