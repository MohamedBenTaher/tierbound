# 45-second demo: shot list

One real run on the hosted project. Two kinds of footage:

- **Automated clips** (about 20 s): `scripts/demo-record.sh` films the public GitHub pages with Playwright at 1280×720. It doesn't sign in, approve or post anything.
- **Your Cursor recording** (about 15–20 s): the part only you can do. Ask Cursor for the pending action, sign in with GitHub on the consent page, read the action and approve it.

`scripts/demo-record.sh compose` stitches them in order and burns in the captions from the table below.

## Before you record (5 minutes)

1. Run `scripts/demo.sh`. It opens a fresh issue, waits for the webhook job, runs the worker until the comment is proposed, and prints the issue number and action id. It approves and posts nothing.
2. So the GitHub sign-in shows up on camera, sign Cursor out of the tierbound MCP server (Cursor Settings → MCP → tierbound → Logout). Also clear site data for `mohamedbentaher.github.io`, or use a fresh Chrome profile.
3. Turn notifications off (Focus mode) and zoom the Cursor chat to about 125%.

## Recording, step by step

```sh
N=<issue number from scripts/demo.sh>
scripts/demo-record.sh before $N    # ~6 s: the issue, no comment yet
scripts/demo-record.sh after $N     # leave running: it waits for the bot comment
```

While `after` waits, record Cursor and the browser with ⌘⇧5 ("Record selected portion"):

1. Ask Cursor: "list my pending tierbound actions". The consent page opens: **Sign in with GitHub** → **Approve**.
2. Then "show that action" (`get_action`): the exact comment and its hash.
3. Then "approve it" (`approve_action` with that hash). Stop recording.

`after` notices the comment about 2 s later, checks that there is **exactly one**, and films the issue scrolling to it. Then:

```sh
scripts/demo-record.sh gate                                   # ~8 s: PR #2's red eval-gate
scripts/demo-record.sh compose ~/Desktop/"Screen Recording ….mov"   # → demo-out/tierbound-demo.mp4
```

Trim the Cursor recording to 15–20 s first (QuickTime: Edit → Trim) so the total lands near 45 s. Clips are in `demo-out/` (git-ignored); each one is also usable on its own in any editor.

## Shots

`compose` reads this table. The **Clip** column decides which segment each caption is shown over; a segment's captions share its time equally, in order.

| Time | Clip | Screen | Caption |
| --- | --- | --- | --- |
| 0:00–0:03 | before | The new issue, top | A new issue lands. The webhook queues a triage job. |
| 0:03–0:06 | before | The issue, scrolled | The worker drafts a reply. It holds no GitHub write credential. |
| 0:06–0:12 | cursor | Cursor → consent page: Sign in with GitHub, Approve | Cursor connects over OAuth 2.1. Only allowlisted GitHub approvers get in. |
| 0:12–0:18 | cursor | Cursor: `list_pending_actions`, `get_action` | I read the exact comment and its hash before anything happens. |
| 0:18–0:24 | cursor | Cursor: `approve_action` | Approval is bound to the hash. Change one byte and it's refused. |
| 0:24–0:31 | after | The issue scrolls to the single bot comment | About 2 s later: exactly one comment, posted by the effector. |
| 0:31–0:39 | gate | PR #2's run: lint ✓, eval-gate ✗, the failing annotation | A worse prompt can't ship: the eval gate fails it in CI. |

Times assume an 18 s Cursor recording; they shift with its length.

## After recording

- Watch `demo-out/tierbound-demo.mp4` once. If a caption sits over the wrong moment, change the rows above and re-run `compose`. Nothing is re-recorded.
- Upload it (YouTube unlisted works, or drag the mp4 into a GitHub issue comment to get a `user-attachments` URL). Then link it from the README "See it run" section and plan step 3.5. Don't commit the mp4: it exceeds the 500 KB pre-commit limit.
- Close or label the demo issue if you don't want it left open.
