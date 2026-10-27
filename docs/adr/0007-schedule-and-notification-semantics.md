# ADR-0007: Schedules skip overlapping firings, use one time zone, and notify per NOTIFY_ON
Status: Accepted

## Context
Schedules start runs automatically (e.g. "0 9 * * 1,4"). A firing can come while the app's previous run is still going, the server can be down when a firing is due, and robfig/cron accepts more syntax than we want users to depend on (`@every`, `CRON_TZ=` prefixes). Run reports go to Slack and email, and a frequent schedule can send a lot of them.

## Decision
- A firing for an app with an active run is skipped and logged, never queued. The existing one-active-run-per-app insert enforces it, so manual and scheduled runs follow the same rule.
- Every schedule fires in the server's TIMEZONE. Expressions are standard 5-field cron plus descriptors (@daily, @weekly, …). `@every` (not tied to the wall clock) and `TZ=`/`CRON_TZ=` prefixes (they would override TIMEZONE) are rejected with a 400.
- Firings missed while the server is down are not caught up. At boot the scheduler loads enabled schedules and waits for their next fire time.
- A firing re-reads its schedule from the database and does nothing if the schedule was deleted or disabled in the meantime, so the database is the source of truth if it and the in-memory entries ever drift apart.
- NOTIFY_ON=all (default) reports every finished run. NOTIFY_ON=failures reports only failed and error runs. POST /api/notify/test ignores NOTIFY_ON.
- Notification errors are logged and never change the run. Email requires STARTTLS, and the Slack webhook URL never appears in logs or API responses.

## Consequences
- No pile-up of queued runs when a suite takes longer than its interval; the cost is a skipped firing, which shows in the log.
- Fire times are easy to reason about (one zone), at the cost of per-schedule zones.
- After downtime, the next run happens at the next regular slot, not immediately.
- Teams that only care about problems can silence passing-run reports without losing failure alerts.
- Providers that only offer implicit TLS (port 465) are not supported.
