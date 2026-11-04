# esn-watch

Push notifications to your phone when ESN Stuttgart lists a new event on
<https://tickets.esn-germany.de/esnstuttgart/>.

It runs on GitHub Actions every 15 minutes (free), reads the ticket shop's
public iCal feed, and sends a push via [ntfy](https://ntfy.sh).

## What you get notified about

| Push | When | Priority |
|---|---|---|
| **New ESN event: …** | An event (or a new date of a series like Stammtisch) appears | high |
| **Tickets available: …** | An event that was *sold out* or *not on sale yet* becomes buyable (presale opens, cancellations) | max (bypasses Do Not Disturb if you allow it) |
| **Date changed: …** | An event you already know about moves | normal |
| **N new ESN events** | More than 5 new events at once (one summary instead of a flood) | high |
| **ESN watcher can't read the ticket shop** | 3 failed checks in a row (sent once, then "recovered" when it's back) | normal |

Tapping a notification opens the event page.

## Setup (~10 minutes)

1. **Install ntfy** on your phone (App Store / Play Store / F-Droid).
2. **Pick a secret topic name.** Anyone who knows it can read your
   notifications, so make it random, e.g. run
   `openssl rand -hex 16` and use `esn-` + the result.
   In the ntfy app: **+** → subscribe to that topic (server `ntfy.sh`).
3. **Create a new public GitHub repo** and push these files to it.
   *Public* is deliberate: public repos get unlimited Actions minutes. A
   private repo at this frequency needs ~2,900 min/month and the free tier
   has 2,000. Nothing secret lives in the repo; the topic is a secret (next step).
4. **Add the secret:** repo → Settings → Secrets and variables → Actions →
   *New repository secret* → name `NTFY_TOPIC`, value = your topic.
5. **Test it:** Actions tab → *ESN watcher* → *Run workflow* → mode
   `test-notify`. Your phone should buzz.
6. **Start it:** *Run workflow* → mode `check`. You'll get one
   "ESN watcher is running – watching N events" push. From then on it runs
   by itself.
7. **Turn on failure emails** (on by default): GitHub → Settings →
   Notifications → Actions → "Send notifications for failed workflows only".

## Things to know

- **GitHub's schedule is not exact.** Runs often start 5–20 minutes late
  and occasionally get skipped. Realistically you'll hear about a new event
  within ~30 minutes.
- **Availability alerts are best effort.** "New event" and "date changed"
  come from the iCal feed, which is solid. "Tickets available" uses the
  JSON endpoint pretix's embeddable widget uses. It isn't a documented API;
  if it changes or disappears, those alerts stop and everything else keeps
  working (the run log will say "availability check skipped").
- **The 60-day rule.** GitHub pauses scheduled workflows in repos with no
  activity for 60 days. The watcher commits `state.json` whenever something
  changes and at least every 25 days, which should keep it alive. If it
  ever does get paused, GitHub emails you and you re-enable it in the
  Actions tab.
- **Pausing:** Actions tab → *ESN watcher* → ⋯ → *Disable workflow*.

## How it works

- `state.json` (committed by the bot) remembers every event seen, keyed by
  pretix's UID (`pretix-esnstuttgart-<event>-<date id>`), never by title.
- First run records everything and only says "watching N events".
- Events that vanish from the feed are marked `gone`, not deleted, so a
  glitchy partial response or a deleted-and-re-added event never
  triggers a second "new" alert.
- Empty or heavily shrunk feeds are rejected for two runs before being
  believed.
- State is only advanced after ntfy confirms delivery, so a failed push is
  retried on the next run instead of lost.

## Local use

```sh
go test ./...
go run . -dry-run          # prints what it would send, writes state.json
NTFY_TOPIC=… go run . -test-notify
```

Optional env vars: `NTFY_SERVER` (self-hosted ntfy), `NTFY_TOKEN`
(protected topic), `ESN_DISABLE_AVAILABILITY=1`, `ESN_ICAL_URL`,
`ESN_WIDGET_URL`.
