## Task tracking (aitodo)

This project uses `aitodo` to track work. Sessions map to working folders, so run commands from the project root.

- Start: `aitodo where --json`. If there is no session, create one: `aitodo session add <name> --dir . --desc "<goal>"`
- Plan: write all steps at once with `aitodo import <<'EOF'` (one task per line; indented bullets become subtasks, indented text becomes the body)
- Loop: `aitodo next --claim --json` (read its comments), do the work, then `aitodo done <id> --note "<one-line result>" --comment "<details: what changed, how verified>"`. Repeat until `next` returns `null`
- Log progress or findings on a task: `aitodo comment <id> "<text>"` (use `-` to read from stdin)
- New work found: `aitodo add "<title>"`, or a subtask with `aitodo sub <parent_id> "<title>"`
- Stuck: `aitodo task block <id> --note "<reason>"`, then continue with `next`
- `go` tasks (a human asked you to start them now) come before `todo` in `next`. If one arrives as a channel event, `aitodo claim <id>` first and ignore the event if that fails
- Full reference: `aitodo manual`
