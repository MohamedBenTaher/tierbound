# pstack (vendored)

This repo vendors [pstack](https://github.com/cursor/plugins/tree/main/pstack) as project skills so coding agents share the same workflows without depending on a user-level install.

| Field | Value |
| --- | --- |
| Plugin version | `0.15.36` |
| Cache hash | `1ba91fc25c0977741dd0e267849e6234b037ed0a` |
| Source | https://github.com/cursor/plugins/tree/main/pstack |
| License | MIT (see `.cursor/pstack-LICENSE`) |
| Skills path | `.cursor/skills/<skill-name>/` |
| Agents path | `.cursor/agents/` (`poteto-agent`, `comment-sicko`) |

## How to update

1. Update the user-level plugin (`/add-plugin pstack` or Cursor's plugin UI), or pull a newer cache under `~/.cursor/plugins/cache/cursor-public/pstack/<hash>/`.
2. Replace `.cursor/skills/` and `.cursor/agents/` with the new plugin's `skills/` and `agents/` directories (copy only; do not edit skill bodies).
3. Refresh `.cursor/pstack-LICENSE`, `.cursor/pstack-VERSION`, and `.cursor/pstack-PLUGIN-HASH`.
4. Commit as `chore: bump vendored pstack to <version>`.

## User-level alternative

If you prefer not to vendor, run `/add-plugin pstack` once per machine. Project skills in `.cursor/skills/` still win for this repo when present.

Do not edit files under `.cursor/skills/` — treat them as upstream.
