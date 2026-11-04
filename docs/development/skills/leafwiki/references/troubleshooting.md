# Troubleshooting

## Exit codes

| Code | Meaning | What to do |
|---|---|---|
| 0 | success | none |
| 1 | unclassified (for `check`: findings to fix) | read the stderr message and the server log |
| 2 | usage | fix the command line; the message names the flag |
| 3 | page not found | check the path, or add `--create` |
| 4 | version conflict | read the page again and retry |
| 5 | not allowed | authenticate (`--user`), or the socket is not ours |
| 6 | unreachable / timeout / restore | retry; a restore is running, or the index is slow |
| 7 | capability missing, or the index could not embed | upgrade both sides (version mismatch), or read the page's `vectors` state |
| 8 | partial batch | the reported items are written; the failing one explains why |
| 75 | the data directory is in use | a server or another process owns it |

## Common cases

**`control channel unavailable`**: the server is not running (or the socket path is elsewhere): the
CLI wrote offline if it could take the lock. If the message says the instance looks like it is
running, exit `75` came back: start the server, or stop whatever holds the directory.

**`the socket belongs to uid N`** (5): the socket is not ours; do not retry offline. Check the data
directory's ownership.

**`permission_denied`** (5): deletes need an editor or an admin account. Read/create/update are
open only on an `optional` channel.

**`totp_required`** (5): the account has TOTP enabled, and the channel cannot ask for a code. Use
another account (an admin without TOTP) for deletes.

**`account_locked`** (5): too many failed logins; the channel shares the web login's lockout
(5 failures lock the account for 15 minutes). Wait, or reset the password from the host.

**`restore_in_progress`** (6): a snapshot restore is swapping the data directory. Retry after it
finishes.

**`method_not_found`** (7): the client and the server are from different releases. Mixed versions
are not supported: restart the server from the same build.

**A write reports `vector:"pending"` and exit 6**: the index did not finish in 30 s. It keeps
working in the background; `--wait 2m` waits longer, and `leafwiki vectors status` shows the state.

**`vectors build` exits 75 while the server runs**: intended. Stop the server, or use
`vectors sync`, which still needs the lock.

**The wiki file changed on disk but the UI does not show it**: the server never watches the file
system. Use the CLI (or a resync: admin "Refresh", `SIGUSR1`/`SIGHUP`). Editing files under a
running server loses the edit on the next save.