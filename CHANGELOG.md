# Changelog

All notable changes to Engram are documented here.

This project follows [Conventional Commits](https://www.conventionalcommits.org/) and uses [GoReleaser](https://goreleaser.com/) to auto-generate GitHub Release notes from commit history on each tag push.

## Where to Find Release Notes

Full release notes with changelogs per version live on the **[GitHub Releases page](https://github.com/Gentleman-Programming/engram/releases)**.

GoReleaser generates them automatically from commits, filtering by type:
- `feat:` / `fix:` / `refactor:` / `chore:` commits appear in the release notes
- `docs:` / `test:` / `ci:` commits are excluded from the generated changelog

## Breaking Changes

Breaking changes are always marked with a `type:breaking-change` label and documented in the release notes with a migration path. The `fix!:` and `feat!:` commit format triggers a major version bump.

## Unreleased

<!-- Changes that are merged but not yet released are tracked here until the next tag. -->

### BREAKING CHANGE: MCP write tools no longer accept a `project` field

The `project` argument has been removed from the JSON schemas of 6 MCP write tools:
`mem_save`, `mem_save_prompt`, `mem_session_start`, `mem_session_end`, `mem_capture_passive`, `mem_update`.

**Before:** agents could pass `project: "my-project"` to write tools.
**After:** the project is auto-detected from the server's working directory (cwd). Any `project` argument sent by the LLM is silently discarded.

**Migration:**
- Remove `project` from write tool calls in your agent's memory protocol.
- Use `mem_current_project` (new tool) to inspect which project Engram will use before writing.
- If the cwd is ambiguous (multiple git repos), Engram returns a structured error with `available_projects`. Navigate to one of the repos before writing.
- Read tools (`mem_search`, `mem_context`, `mem_timeline`, `mem_get_observation`, `mem_stats`) still accept an optional `project` override — validated against the store.

### New tool: `mem_current_project`

Returns detection result including `project`, `project_source`, `project_path`, `cwd`, `available_projects`, and `warning`. Never errors — returns success even when the cwd is ambiguous. Recommended as the first call when starting a session to confirm which project will receive writes.

- **feat(project):** add project name auto-detection via git remote and normalization (lowercase + trim + collapse) on all read/write paths
- **feat(cli):** add `engram projects list|consolidate|prune` commands for project hygiene
- **feat(mcp):** add `mem_merge_projects` tool for agent-driven project consolidation
- **feat(mcp):** auto-detect project at MCP startup via `--project` flag, `ENGRAM_PROJECT` env, or git remote
- **feat(mcp):** similar-project warnings when saving to a new project that resembles an existing one
- **fix(sync):** use git remote detection instead of `filepath.Base(cwd)` for project name
