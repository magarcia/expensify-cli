---
name: expensify
description: Use the Expensify CLI to manage expenses, reports, and workspaces. Learn commands and flags through the CLI's built-in help.
---

Use `expensify` from PATH, or `./bin/expensify` in a source checkout.

Discover the installed interface before constructing commands:

1. `expensify --help` lists resources and global flags.
2. `expensify <resource> --help` lists actions.
3. `expensify <resource> <action> --help` shows flags and examples.

Consult `expensify help auth`, `expensify help environment`, or `expensify help exit-codes` when relevant. Use the installed help as the command reference.

- Run agent commands with `--no-input --output llm`. Read the labeled `- field: value` lines and `Record N` sections, including pagination metadata. Check the exit code; errors go to stderr.
- Handle authentication through the CLI using an authorized login method. Never print credentials.
- Preview writes with `--dry-run`; use `--yes` only for actions the user authorized.
- After a failed write, inspect the resource before retrying.

Example after reading the command's help:

```sh
expensify expense list --limit 20 --no-input --output llm
```
