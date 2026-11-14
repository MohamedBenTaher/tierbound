# 45-second demo: shot list

One real run on the hosted project, recorded in one take per shot and cut
together. Captions sit at the bottom of the frame; keep each on screen for
the whole shot.

## Before you record (5 minutes)

1. Run `scripts/demo.sh`. It opens a fresh issue, waits for the webhook job,
   runs the worker until the comment is proposed, and prints the issue URL and
   action id. It approves and posts nothing.
2. To film the GitHub sign-in, sign Cursor out of the tierbound MCP server
   (Cursor Settings → MCP → tierbound → Logout) and clear site data for
   `mohamedbentaher.github.io` (or use a fresh Chrome profile). Otherwise
   Cursor and the consent page reuse their sessions and skip shot 3.
3. Open the tabs: the new issue, the tierbound repo, and
   [PR #2 checks](https://github.com/MohamedBenTaher/tierbound/pull/2/checks).
4. Record with ⌘⇧5 (record selected portion), 1080p, notifications off
   (Focus mode). Zoom the browser and Cursor chat to about 125%.

## Shots

| Time | Screen | Action | Caption |
| --- | --- | --- | --- |
| 0:00–0:04 | Title card: README header (logo + tagline) | none | **tierbound**: agents propose, a human approves the exact bytes, it happens once. |
| 0:04–0:10 | The new GitHub issue | Scroll the issue once | A new issue lands. The webhook queues a triage job. |
| 0:10–0:15 | Terminal with the end of `scripts/demo.sh` | Hold on `proposed action …` | The worker drafts a reply. It has no GitHub write credential. |
| 0:15–0:23 | Cursor chat → browser consent page | Ask "list my pending tierbound actions". Cursor opens the consent page: **Sign in with GitHub** → **Approve** | Cursor connects over OAuth 2.1. Only allowlisted GitHub approvers get in. |
| 0:23–0:30 | Cursor chat | `list_pending_actions` result, then "show that action" (`get_action`) | I read the exact comment and its hash before anything happens. |
| 0:30–0:35 | Cursor chat | "approve it" (`approve_action` with that hash) | Approval is bound to the hash. Change one byte and it's refused. |
| 0:35–0:40 | The issue, refreshed | One comment from tierbound-mbt appears | About 2 s later: exactly one comment, posted by the effector. |
| 0:40–0:45 | PR #2 checks | Hover the red `eval-gate` | A worse prompt can't ship: the eval gate fails it in CI. |

Order note: Cursor's OAuth sign-in happens at its first MCP call, so the
consent page appears inside shot 4 (the "list" request), before
`get_action` and the approval.

## After recording

- Trim to 45 s and burn in the captions (iMovie titles, CapCut or ScreenFlow).
- Upload it (YouTube unlisted works, or drag the mp4 into a GitHub issue
  comment to get a `user-attachments` URL). Then link it from the README
  "See it run" section and plan step 3.5. Don't commit the mp4: it exceeds
  the 500 KB pre-commit limit.
- Close or label the demo issue if you don't want it left open.
