# aitodo manual (for AI agents and scripts)

aitodo is a persistent TODO list for AI agents: **sessions → tasks**.
Data lives in SQLite (`$AITODO_DB` or `~/.aitodo/aitodo.db`), so the state survives across
conversations, and a human can watch progress live in the TUI (`aitodo`), which auto-refreshes.

- **Session**: a unit of work, such as a feature, bug or project. It has a unique `name`, an optional `description`
  and an optional **workdir**, the project folder the session belongs to.
- **Task**: an ordered item inside a session. It has a `title`, an optional `body` (details), a `status` and a `note`
  (a short result summary or blocker reason, overwritten on update). Task IDs are globally unique integers.
- **Subtask**: a task with a `parent_id`. Nesting can go to any depth. `next` only returns tasks whose subtasks are all
  finished (`done`/`skipped`), so children are worked first and the parent comes back last as a final check.
- **Comment**: an append-only log entry on a task, with an `author` and a timestamp. Use comments for progress,
  findings, detailed results, and questions for the human. Humans see them live in the TUI.

| status    | mark  | meaning                                  |
|-----------|-------|------------------------------------------|
| `todo`    | `[ ]` | not started                              |
| `doing`   | `[>]` | in progress                              |
| `done`    | `[x]` | finished (counts as complete)            |
| `skipped` | `[-]` | intentionally not done (counts as complete) |
| `blocked` | `[!]` | cannot proceed. Explain why in `note`.   |

## Session resolution

Commands that act on a session take `-s/--session REF`, where REF is a session ID or its exact name. If you omit it,
the session is resolved in this order:

1. `$AITODO_SESSION`
2. **the current directory**: the active session whose workdir is the cwd or a parent of it. The longest match wins.

So after `aitodo session add my-feature --dir .`, every command run inside that folder (or a subfolder)
targets `my-feature` automatically.

## Recommended agent loop

```sh
aitodo where --json                      # which session am I in? (error if none)
aitodo session add fix-login --dir . --desc "Fix OAuth login redirect loop"   # if none
aitodo import <<'EOF'                    # write the plan: one task per line
- Reproduce the redirect loop
- Find the cause in auth middleware
    indented plain lines become the task body
- Fix and add a regression test
    - Patch middleware            (indented bullets become subtasks)
    - Add regression test
- Run the full test suite
EOF

aitodo next --claim --json               # take the next task (todo -> doing), returns null when finished
# ... do the work ...
aitodo comment 12 "Found: SameSite=Strict drops the cookie on the OAuth callback"   # progress log
aitodo done 12 --note "Fixed cookie SameSite" --comment "Changed middleware/auth.go; added test_auth_redirect; all tests pass"
aitodo next --claim --json               # repeat until it prints null
```

Rules of thumb:
- Write the whole plan up front with `import`, and add tasks you discover as you go (`aitodo add "..."`).
  Break big tasks down with subtasks: `aitodo sub PARENT_ID "..."`, or nested bullets in `import`.
- Always leave a `--note` (one line) when you finish or block a task. Put the details (what changed, where, how you
  verified it, follow-ups) in a comment: `aitodo done ID --note "..." --comment "..."` or `aitodo comment ID "..."`.
- Read the existing comments before you start a task. `next` and `task show` include them.
- If you cannot proceed, run `aitodo task block ID --note "why"` and move on to the next task.
- `next` returns the `doing` task first, so an interrupted agent resumes where it stopped.
- **Parallel agents** on one session should use `aitodo next --claim --fresh`, so each one gets a distinct task.
- Use `--json` for machine-readable output. On failure the exit code is non-zero and `{"error": "..."}` goes to stderr.

## Command reference

Global options: `--json`, `--db PATH`. Setting `AITODO_JSON=1` enables `--json` everywhere.

### Sessions

| command | description |
|---|---|
| `aitodo session add NAME [--dir PATH] [--desc TEXT]` | create a session (`--dir .` = current folder) |
| `aitodo session list [--all]` | list active sessions (`--all` includes archived ones) |
| `aitodo session show [REF]` | a session with all its tasks. JSON: `{"session":{...},"tasks":[...]}` |
| `aitodo session edit [REF] [--name N] [--dir PATH] [--desc TEXT]` | update fields (`--dir ""` clears the workdir) |
| `aitodo session archive [REF]` / `unarchive [REF]` | hide or restore a session |
| `aitodo session rm REF` | delete a session and its tasks (REF is required) |
| `aitodo session current [--dir PATH]` (alias `aitodo where`) | resolve the session from a directory |
| `aitodo status [-s REF]` | progress summary plus the next task |

### Tasks

| command | description |
|---|---|
| `aitodo add [-s REF] TITLE [--body TEXT] [--parent ID]` | append a task (or a subtask of `--parent`) |
| `aitodo sub PARENT_ID TITLE [--body TEXT]` | append a subtask |
| `aitodo import [-s REF] [--parent ID] [--file F]` | append many tasks from stdin or a file (see the formats below) |
| `aitodo ls [-s REF] [--status todo,doing] [--pending]` | list tasks in order |
| `aitodo task show ID` | one task with its body, note, direct subtasks and comments |
| `aitodo next [-s REF] [--claim] [--fresh]` | the next task (with its comments and subtasks), considering only tasks without unfinished subtasks: `doing` first (so an interrupted agent resumes), else the first `todo`. `--claim` marks it `doing` atomically. `--fresh` skips `doing` tasks and always takes a new `todo` (for several agents in parallel). Prints `null` (JSON) when nothing is left |
| `aitodo start ID...` | mark `doing` |
| `aitodo done ID... [--note TEXT] [--comment TEXT]` | mark `done`. `--comment` also appends a comment (works for all status commands) |
| `aitodo task skip ID... [--note TEXT]` | mark `skipped` |
| `aitodo task block ID... [--note TEXT]` | mark `blocked` |
| `aitodo task reopen ID...` | mark `todo` again |
| `aitodo task status STATUS ID... [--note TEXT]` | set any status |
| `aitodo task edit ID [--title T] [--body B] [--note N] [--parent ID\|0]` | edit fields. `--parent` moves the task under another task (`0` = top level) |
| `aitodo task move ID (--up\|--down\|--top\|--bottom\|--to INDEX)` | reorder among siblings (INDEX is 0-based) |
| `aitodo task rm ID...` | delete, including subtasks and comments (prefer `skip` to keep history) |

### Comments

| command | description |
|---|---|
| `aitodo comment ID TEXT... [--author NAME]` | append a comment. If TEXT is `-`, it is read from stdin (for multi-line text) |
| `aitodo comment list ID` (or `aitodo comment ID`) | list comments, oldest first |
| `aitodo comment rm COMMENT_ID` | delete a comment |

The author defaults to `$AITODO_AUTHOR`, then `"ai"`. Set `AITODO_AUTHOR=claude` (etc.) to tell agents apart.
Comments written from the TUI use `$USER`.

### Import formats

Text: one task per line. Markdown bullets and checkboxes (`- `, `* `, `1. `, `- [ ] `) are stripped.
**Indented bullet lines become subtasks** of the nearest less-indented task, to any depth. Indented lines without a bullet
are appended to the previous task's body. Blank lines and lines starting with `#` are ignored.

JSON: an array whose items are either strings or `{"title": "...", "body": "...", "subtasks": [...]}` objects.
The `subtasks` arrays can be nested and use the same item format.

```sh
echo '[{"title":"Write tests","body":"cover edge cases","subtasks":["unit","e2e"]},"Update docs"]' | aitodo import -s my-feature --json
```

### JSON shapes

```json
// session
{"id":1,"name":"fix-login","description":"...","workdir":"/abs/path","status":"active",
 "created_at":"2026-09-27T03:00:00Z","updated_at":"...","total":4,"done":1,"doing":1}
// task (list items; "depth" is the tree depth in `ls` output)
{"id":12,"session_id":1,"parent_id":null,"title":"...","body":"...","status":"doing","note":"","position":2,
 "created_at":"...","updated_at":"...","started_at":"...","done_at":null,
 "depth":0,"subtasks_total":2,"subtasks_done":1,"comment_count":3}
// task detail (task show / next): the task fields plus
 "subtasks":[{task}...], "comments":[{"id":1,"task_id":12,"author":"ai","body":"...","created_at":"..."}]
```

## MCP server

`aitodo mcp` runs an MCP server over stdio. Register it with Claude Code:

```sh
claude mcp add aitodo -- aitodo mcp            # current project
claude mcp add -s user aitodo -- aitodo mcp    # all projects
```

The same JSON config works for other clients: `{"command": "aitodo", "args": ["mcp"]}`.
You can add `"env": {"AITODO_DB": "/path/to.db"}`.

Tools: `session_list`, `session_create`, `session_get`, `session_update`, `task_add` (`parent_id`),
`task_add_bulk` (nested `subtasks`, `parent_id`), `task_list`, `task_get` (with subtasks and comments), `task_next`,
`task_start`, `task_done` (`note`, `comment`), `task_set_status`, `task_comment`, `task_comments`, `task_edit`
(`parent_id`), `task_move`, `task_delete`.

Session-scoped tools accept `session` (ID or name) or `workdir`. If you pass neither, the session is resolved from
the server's cwd, which is normally the project directory.

## TUI (for humans)

Run `aitodo`, or `aitodo tui -s REF`. It auto-refreshes every 1.5 s, so you can watch an agent check tasks off.
Subtasks are shown as an indented tree, with `done/total` and `✎N` (the comment count) at the right.
Mouse: click to select, click `[ ]` to toggle done, double-click a task to edit it, use the wheel to scroll,
drag the detail pane's top border to resize it (remembered across restarts), right-click a row or empty pane space for a context menu, and click the buttons in the bottom bar.
Keys: `tab` switches panes, `j/k` moves, `space` toggles done, `s` doing, `b` blocked, `-` skipped, `a` adds a task, `A` adds a subtask, `c` comments,
`v` opens the full task view (body, subtasks, all comments),
`n` creates a session, `e` edits, `w` sets the workdir (Tab completes folders like bash), `d` deletes, `J/K` reorders, `f` hides done tasks,
`z` archives, `H` shows archived sessions, `q` quits.

## Web UI (for humans)

`aitodo web [--addr HOST:PORT] [-s REF] [--open]` serves a browser UI (default `http://127.0.0.1:7878`) with the
same operations as the TUI. It auto-refreshes every 1.5 s; press `?` for its keys. It listens on loopback only by
default and has no authentication. Agents should keep using the CLI or MCP rather than the web API.
