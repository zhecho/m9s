# m9s

A terminal-based email client with a three-pane TUI and local draft storage.
Each account uses one of three providers:

| Provider | `provider:` | Talks to | Sign-in |
|----------|-------------|----------|---------|
| IMAP/SMTP | `imap` (default) | any IMAP and SMTP server | password (env, KeePassXC, command, config or Keychain) — see [IMAP/SMTP accounts](#imapsmtp-accounts) |
| Gmail | `gmail` | the Gmail REST API | OAuth, secrets in KeePassXC — see [Gmail (API)](#gmail-api) |
| Microsoft 365 / Outlook.com | `microsoft` | Microsoft Graph | OAuth, token in KeePassXC — see [Microsoft 365 / Outlook.com (Graph API)](#microsoft-365--outlookcom-graph-api) |

Accounts of all three kinds can be configured side by side and switched
with `[` / `]`.

## Contents

- [Screen](#screen)
  - [Connection status](#connection-status)
- [Installation](#installation)
  - [From source](#from-source)
  - [Version](#version)
- [Configuration](#configuration)
  - [Config file and `m9s check`](#config-file-and-m9s-check)
  - [Turning an account off](#turning-an-account-off)
  - [Editing the config from m9s](#editing-the-config-from-m9s)
  - [Settings for every account](#settings-for-every-account)
  - [Themes](#themes)
  - [KeePassXC setup](#keepassxc-setup)
  - [Passwords at startup](#passwords-at-startup)
- [IMAP/SMTP accounts](#imapsmtp-accounts)
  - [Setup (one time)](#setup-one-time)
  - [Config](#config)
  - [Credentials](#credentials)
  - [Signing in](#signing-in)
  - [Using IMAP in m9s](#using-imap-in-m9s)
- [Gmail (API)](#gmail-api)
  - [Setup (one time)](#setup-one-time-1)
  - [Config](#config-1)
  - [Credentials](#credentials-1)
  - [Signing in](#signing-in-1)
  - [Using Gmail in m9s](#using-gmail-in-m9s)
- [Microsoft 365 / Outlook.com (Graph API)](#microsoft-365--outlookcom-graph-api)
  - [Setup (one time)](#setup-one-time-2)
  - [Config](#config-2)
  - [Credentials](#credentials-2)
  - [Signing in](#signing-in-2)
  - [Using Outlook in m9s](#using-outlook-in-m9s)
- [Keybindings](#keybindings)
  - [Keys](#keys)
  - [Move, archive, star, forward](#move-archive-star-forward)
  - [Selecting several messages](#selecting-several-messages)
  - [Deleting in Deleted Items](#deleting-in-deleted-items)
  - [`:` command prompt](#-command-prompt)
  - [`/` filter](#-filter)
  - [Mail list order](#mail-list-order)
  - [Reading a message in the browser](#reading-a-message-in-the-browser)
  - [Attachments](#attachments)
  - [Custom keybindings](#custom-keybindings)
- [CLI Commands](#cli-commands)
  - [`m9s check`](#m9s-check)
  - [`m9s login` / `m9s logout`](#m9s-login--m9s-logout)
  - [`m9s send`](#m9s-send)
- [Local Draft Storage](#local-draft-storage)
- [Roadmap](#roadmap)
- [License](#license)

## Screen

```text
Account:  personal            <j/k> Navigate     </> Filter   <r> Reply           __  __  ___
User:     you@example.com     <g/G> Top/Bottom   <?> Help     <R> Reply all      |  \/  |/ _ \ ___
Folder:   INBOX               <tab> Next pane    <q> Quit     <d> Delete         | |\/| | (_) / __|
Unread:   3 of 128            <[ ]> Account                   <u> Unread         | |  | |\__, \__ \
Status:   ● connected         <:>   Command                   <b> Browser        |_|  |_|  /_/|___/
Provider: imap / v1.0.0       <c>   Compose                   <s> Save
RSRV:     Dovecot / -         <S>   Sync                      <o> Open
                              <l>   Logs
┌─ FOLDERS ───────────┬─ MAIL LIST ────────────────────────────────────────────────────────────┐
│ personal            │ ● Alice     Re: Meeting tomorrow                          Nov 11 10:23 │
│  INBOX (3)          │ ● Bob       Deploy done                                   Nov 11 09:41 │
│  Sent               │   Carol     Lunch plans?                                  Nov 10 18:02 │
│  Drafts             ├─ PREVIEW ──────────────────────────────────────────────────────────────┤
│  Trash              │ From: Alice <alice@example.com>                                        │
│  Local Drafts       │ Can we move the meeting to 3pm?                                        │
└─────────────────────┴────────────────────────────────────────────────────────────────────────┘
 <personal>  <INBOX>                                                              Sync complete
```

- **Top bar** — the active account, its user, the open folder, that folder's
  unread and total message counts, the connection status, the provider with
  the m9s version (`Provider:`), and the server's name and version (`RSRV:`):
  what an IMAP server reports about itself (`-` when it tells none),
  `Gmail API / v1 (rev …)` for Gmail, `Microsoft Graph / v1.0` for Microsoft.
  Beside it are the
  keys that work right now; the last column changes with the focused pane. On a
  narrow terminal the logo is dropped first, then key columns from the right.
- **Folders** (left) — the folder tree of the active account, with the account
  name at its top. Every configured account is available from startup, even
  if it cannot connect; switch with `[` / `]` or
  `:<account>`. Moving the cursor onto a folder opens it; `enter` opens it
  immediately.
- **New mail** — m9s keeps an IMAP IDLE connection on INBOX, so new or
  removed messages there show up immediately (the list reloads in place,
  keeping your cursor and filter, and INBOX's count updates). Other folders
  refresh their counts on the `badge_poll` interval or with `S`. Gmail and
  Microsoft accounts have no IDLE: they ask the server what changed every
  `badge_poll` (default 30 s).
- **Mail list / preview** (right) — a message is marked as read when you move
  onto it in the mail list (its `●` goes at once), not while you browse
  folders. `u` marks it unread again.
- **Resizing the preview** — drag the border between the mail list and the
  preview up or down with the mouse. The size you leave is remembered in
  `~/.local/state/m9s/layout.json` and used next time; `ui.mail_list_rows`
  (default 10) is the size before you ever drag.
- **Selecting text** — drag with the left mouse button to select text inside
  one pane; when you let go it is copied to the clipboard (`Copied N
  characters` on the bottom row). A click without dragging works as before.
- **Local copy of all mail** — m9s keeps a full copy of every account under
  `~/.local/share/m9s/mail/` (under `$XDG_DATA_HOME` when set): an SQLite
  index (`index.db`) plus one raw `.eml` file per message:
  `<account>/<folder>/<uid>.eml` for IMAP, `<account>/<id>.eml` for Gmail and
  Microsoft (a message under several labels is kept once). Folder lists, mail
  lists and messages are shown from this copy at once — also at startup and
  without a connection — and refreshed when the server answers. The copy is
  filled in the background (for IMAP over a separate connection per account):
  headers first, then message content, INBOX and the newest first; the Status
  row shows `saving mail 1200/53000` while it runs. Afterwards an IMAP account
  is compared with the server every 5 minutes and whenever new mail arrives in
  INBOX; Gmail and Microsoft ask what changed every `badge_poll` and fetch
  missing content every `body_cache_poll`. So mail deleted or read elsewhere
  is updated here too. The copy is not encrypted (files are readable by you
  only) and needs as much disk as your mailboxes; delete the directory at any
  time and it is downloaded again. Keep it off iCloud/Dropbox/network drives.
- **Bottom row** — where you are (`<account> <folder>`, plus `</filter>` when a
  filter is active) and transient messages.

### Connection status

| Status | Meaning |
|--------|---------|
| `… connecting` | connecting, or retrying |
| `● connected` | folder list received |
| `✗ <reason> · retry in 8s` | the connection failed or was lost; m9s retries by itself |
| `✗ auth failed` / `✗ no password` | credentials were rejected or missing; not retried automatically |
| `✗ sign in again` | Gmail or Microsoft no longer accepts the saved sign-in; run `m9s login <account>` in another terminal, then press `S` |

m9s reconnects automatically: after a failure it retries after 2 s, doubling
up to 60 s, until the server is reachable again — also when a working
connection drops (network change, laptop sleep). Your open folder, cursor and
filter are kept. Press `S` to retry immediately. Wrong credentials are not
retried automatically, because repeated bad logins can lock a mailbox.
Passwords are looked up once, when m9s starts (see
[Passwords at startup](#passwords-at-startup)), so after fixing one — in
KeePassXC, the config, the Keychain or wherever it lives — **restart m9s**;
`S` only tries again with the password it already has.

The first failure and the recovery are reported on the bottom row; every
attempt is written to the log (`l`).

## Installation

### From source

Requires Go 1.26 or newer.

```bash
go install github.com/zhecho/m9s@latest
```

Or from a clone:

```bash
git clone https://github.com/zhecho/m9s
cd m9s
go install .
```

The binary is placed in `$GOPATH/bin/m9s` (or `~/go/bin/m9s`).

### Version

`m9s --version` prints the version, and the top bar shows it next to the
account's provider (`Provider / Version`). Releases are git tags (`v1.0.0`).
Built from a clean checkout of a tag, the binary reports that tag; built from
anywhere else, Go's pseudo-version (`v1.0.1-0.<date>-<commit>`, plus `+dirty`
with uncommitted changes); `dev` when Go has no version information. To set
it by hand:

```bash
go build -ldflags "-X github.com/zhecho/m9s/internal/version.Version=v1.0.0" .
```

## Configuration

These settings apply to every account, whatever its provider. Each account
type then has its own section with the same parts, in the same order:
[IMAP/SMTP accounts](#imapsmtp-accounts), [Gmail (API)](#gmail-api) and
[Microsoft 365 / Outlook.com](#microsoft-365--outlookcom-graph-api).

### Config file and `m9s check`

Config file location: `~/.config/m9s/config.yaml` (another one with `-c <path>`).

Run `m9s check` after editing your config: it verifies IMAP and SMTP
connectivity, or the Gmail / Microsoft sign-in, for every account.

### Turning an account off

`enable: false` on an account leaves it out entirely: m9s does not load it,
and its other settings are not checked, so a half-finished account cannot stop
m9s from starting. `enable: true`, or no `enable` line, loads it. If every
account is disabled, m9s does not start.

### Editing the config from m9s

`:config` opens the config file (the one given with `-c`, or the default) in
`ui.editor`, or `$EDITOR` if that's not set. Save and quit, and m9s checks it:

- **valid and changed** — the file is replaced and m9s restarts itself with
  it (same binary and flags), so every setting applies, accounts included.
  The startup passwords window shows briefly again;
- **unchanged**, or the editor exits with an error (vim `:cq`) — nothing
  happens;
- **invalid** (bad YAML, or no account m9s can start) — the file is left as
  it was and a dialog shows the error: **Edit again** reopens your edit,
  **Discard** (or `esc`) drops it. m9s keeps running with the old config.

The editor works on a copy next to the file, and the file is only replaced
once the copy checks out. Its permissions are kept, and a symlinked config
stays a symlink.

### Settings for every account

Each account has these keys whatever its provider, next to the provider's
own block (`imap:` / `smtp:`, `gmail:` or `microsoft:`, see the account
sections below). `ui:` is for m9s as a whole.

```yaml
accounts:
  - name: personal
    enable: true              # false leaves the account out, as if it were not here (default true)
    provider: imap            # imap (default), gmail or microsoft
    keepassxc: true           # take secrets from the running KeePassXC (see "KeePassXC setup")
    folders:
      auto_detect: true       # discover INBOX/Sent/Drafts/Trash/Spam by IMAP attributes
      badge_poll: 60s         # how often to refresh unread counts (default: 60s; 30s for Gmail and Microsoft)
      body_cache_poll: 10m    # Gmail and Microsoft: how often missing message content is fetched (default: 10m)
      include:                # folders to show besides the auto-detected ones
        - name: "Work"
        - name: "Archive"
      exclude:
        - "Spam"              # suppress this folder from the sidebar
      archive: "Archive/2026" # where `a` archives to on IMAP and Microsoft (default: the server's archive folder)

ui:
  theme: solarized-dark       # see "Themes" below (legacy key: skin)
  colors:                     # optional per-role overrides on top of the theme
    accent: "#ff79c6"
  editor: $EDITOR             # editor command for composing mail; falls back to $EDITOR env var
  date_format: "Jan 02 15:04" # Go time format string
  mail_list_rows: 10          # messages visible in the mail list; the preview gets the rest (default: 10)
  attachment_dir: ~/Downloads # where s saves attachments without asking (default: ask each time)
```

### Themes

Built-in themes: `solarized-dark` (default), `solarized-light`, `dracula`,
`gruvbox-dark`, `nord`, `monokai`.

If `ui.theme` is not a built-in name, m9s loads `~/.config/m9s/themes/<name>.yaml`:

```yaml
name: mine
colors:
  background: "#101010"
  accent:     "#ff8800"
```

Roles a theme leaves out keep the `solarized-dark` colour. `ui.colors` overrides
single roles on top of whichever theme is active. Roles: `background`, `foreground`,
`unread`, `selected_bg`, `selected_fg`, `accent`, `dim`, `header_bg`, `header_fg`,
`statusbar_bg`, `label`, `border`, `flash_ok`, `flash_err`, `warn` (log warnings),
`heading` (yellow: the top bar's labels, the logo and the keys in `:help`). Values are `#rrggbb`,
a colour name (`red`, `navy`, …) or `default`.

An unknown theme, unknown role or invalid colour does not stop m9s: the default is
used and a warning is shown in the status bar and the log (`l`).

### KeePassXC setup

m9s can take secrets — IMAP/SMTP passwords, the Gmail client and token, the
Microsoft token — from the **running** KeePassXC app instead of from the
config. KeePassXC does the unlocking itself — with Touch ID where you have it
set up — so m9s never sees or asks for the master password or key file.
(Touch ID needs one unlock with the master password after KeePassXC starts;
that is KeePassXC's rule.) Which entries each account needs is in its
**Credentials** section.

One-time setup:

1. In KeePassXC: **Settings → Browser Integration → Enable browser
   integration**. You do not need to tick any browser. m9s talks to KeePassXC
   over the same local channel the browser extension uses.
2. In the m9s config, add `keepassxc: true` to the account.
3. Start m9s. The first time, KeePassXC asks you to allow a new connection
   and name it: type `m9s`. That approval is remembered in the macOS Keychain
   (service `m9s`, account `keepassxc/association`), so you are asked once.

If KeePassXC is locked, it shows its unlock window and m9s waits (Esc to
skip). The first time m9s asks for an entry, KeePassXC may also ask you to
allow access to it (tick **Remember** so it does not ask again); if that takes
more than a second the window says so, and Esc or the 2-minute limit apply
there too. If KeePassXC is not running or browser integration is off, the
`keepassxc` column says so and the next source is tried.

### Passwords at startup

Every time m9s starts, before the main screen, a window shows where each
account's passwords are coming from:

```text
┌─ m9s · getting passwords ────────────────────────────────────────────────────────────────┐
│ Tried in this order, first one found wins:                                               │
│                                                                                          │
│ account          1 env          2 keepassxc    3 password_cmd  4 plain text   5 keychain │
│ mitko-example    –              ✔ used         –               –              ·          │
│ e2e-example      ✔ used         ·              –               –              ·          │
│ survey           –              … asking       –               waiting        waiting    │
│                                                                                          │
│ KeePassXC is locked — unlock it in the KeePassXC window…                                 │
│ esc  skip KeePassXC                                                                      │
│                                                                                          │
│ – not set    · not needed, already found    ✔ used    ✗ failed                           │
└──────────────────────────────────────────────────────────────────────────────────────────┘
```

How it ends:

| What happened | What you see |
|---|---|
| Every account has its passwords and nothing failed | The window flashes by and m9s opens. |
| m9s is waiting for KeePassXC (unlock, or approving m9s) | The window stays until KeePassXC answers. **Esc** stops waiting and moves on to the next source; after 2 minutes it gives up by itself. |
| Every account works, but a source failed on the way (e.g. KeePassXC not running, so the plain `password` was used) | What failed is listed; m9s opens after 5 s or any key — so you notice you are on a fallback. |
| Some accounts have no password | They are listed with the reason; m9s opens **without them** after 5 s or any key. |
| No account has a password | The reasons are listed and m9s closes after 8 s (or any key) with exit status 1. |

`q` or Ctrl-C in the window quits without starting. Every failure is also
written to the log (`l`); passwords never are.

## IMAP/SMTP accounts

An account with `provider: imap` (the default) talks to any IMAP server for
reading and any SMTP server for sending, with a password for each. For a
Gmail or Outlook.com / Microsoft 365 mailbox, use its own provider instead:
[Gmail (API)](#gmail-api) or
[Microsoft 365 / Outlook.com](#microsoft-365--outlookcom-graph-api).

### Setup (one time)

From your mail provider's help pages (or your IT), note:

- the **IMAP** server name and port: 993 with TLS, or 143 with STARTTLS;
- the **SMTP** server name and port: 465 with TLS, or 587 with STARTTLS;
- the **user name**, usually your full address, and the password. Some
  providers want an app password instead of your normal one when two-step
  sign-in is on.

### Config

```yaml
accounts:
  - name: personal
    provider: imap            # the default: may be left out
    keepassxc: true           # take both passwords from the running KeePassXC (see "Credentials")
    imap:
      host: mail.example.com
      port: 993
      tls: true               # true = implicit TLS (port 465/993)
                              # false = STARTTLS (port 587/143)
      tls_skip_verify: false  # set true only for self-signed certs
      user: you@example.com
      password: ""            # plain text — use password_cmd instead
      password_cmd: "pass email/personal"  # shell command, output used as password
    smtp:
      host: mail.example.com
      port: 587
      tls: false              # false = STARTTLS on port 587
      user: you@example.com
      password_cmd: "pass email/personal"

  - name: work
    provider: imap            # the default: may be left out
    imap:
      host: mail.company.com
      port: 993
      tls: true
      user: you@company.com
      password_cmd: "pass email/work"
    smtp:
      host: mail.company.com
      port: 587
      tls: false
      user: you@company.com
      password_cmd: "pass email/work"
```

The keys every account has (`enable`, `folders`, …) are in
[Settings for every account](#settings-for-every-account).

### Credentials

For each account and each service (IMAP, SMTP) these are tried in order; the
first one that gives a password wins:

1. **env** — `<ACCOUNT>_IMAP_PASSWORD` / `<ACCOUNT>_SMTP_PASSWORD`, then
   `<ACCOUNT>_IMAP_PASSWORD_CMD` / `<ACCOUNT>_SMTP_PASSWORD_CMD` (run as a shell command)
2. **keepassxc** — the running KeePassXC, if the account has `keepassxc: true`
3. **password_cmd** — the field in config (run as a shell command)
4. **plain text** — the `password` field in config
5. **keychain** — the macOS Keychain entry stored by `m9s login`

`<ACCOUNT>` is the account name uppercased with non-alphanumeric characters
replaced by `_`. E.g., account name `my work` → `MY_WORK`.

A command (`*_PASSWORD_CMD`, `password_cmd`) may run for at most 2 minutes.
A source that is set but fails (KeePassXC not running, a command that exits
with an error or runs too long, …) does not stop the search: it is shown as failed and the next
one is tried. An account is used only if **both** its IMAP and SMTP passwords
were found. `m9s check` prints which source each password came from, and why
any source failed.

With `keepassxc: true` (see [KeePassXC setup](#keepassxc-setup)), each mail
entry in KeePassXC needs:

- **URL**: the mail server, as `https://<host>` — the `host` from the
  account's `imap:` (and `smtp:`) section, e.g. `https://mail.example.com`;
- **User name**: the account's `user` (case and surrounding spaces do not
  matter). Keep `user` under `imap:` and `smtp:` — it is how m9s tells
  entries on the same server apart.

For each service m9s asks KeePassXC for the entries on that host and takes
the one whose user name equals the service's `user`. Several accounts on one
server are fetched with one request. If no entry matches, or two do, the
window says so and the next source is tried.

### Signing in

An IMAP/SMTP account signs in with its password each time it connects. To
keep the passwords in the macOS Keychain, leave `password`, `password_cmd`
and `keepassxc` out of the account and run:

```sh
m9s login work            # prompts once, verifies IMAP + SMTP, stores both
m9s login work --smtp     # only the SMTP password (also --imap)
m9s login work --no-verify
m9s logout work           # remove the entries
echo "$PW" | m9s login work   # non-interactive: reads stdin when it is not a terminal
```

Entries live in the login keychain under service `m9s`, account `<name>/imap` and
`<name>/smtp` — visible in Keychain Access (the value is stored base64-encoded by
go-keyring, so read it through m9s rather than `security -w`). Nothing is stored if
verification fails.

Passwords are looked up once, at startup; after changing one, restart m9s.

### Using IMAP in m9s

- The server's folders are the folders. With `folders.auto_detect`, the
  special ones (INBOX, Sent, Drafts, Trash, Spam) are found by the folder
  attributes the server sets.
- New mail in INBOX shows up at once (IMAP IDLE); other folders refresh their
  counts every `badge_poll` or with `S`.
- Every row is one message (no conversations). `*` sets or clears the
  `\Flagged` flag;
  `a` archives to `folders.archive`, else the server's `\Archive` folder,
  else one called `Archive`; `m` moves to another folder.
- `/` filters the list as you type; `enter` then runs a text search on the
  server; `esc` returns to the folder.
- Sending goes out over SMTP; a server draft is replaced when you edit it.

## Gmail (API)

An account with `provider: gmail` talks to the Gmail REST API instead of IMAP/SMTP:
labels are folders, mail is grouped in conversations, and sending goes through
the API. Sign-in is OAuth, so you need a (free) Google Cloud project once.
Everything secret lives in KeePassXC, so `keepassxc: true` is required.

### Setup (one time)

No billing account is needed.

1. Create a project and enable the Gmail API (in Cloud Shell:
   `gcloud services enable gmail.googleapis.com`).
2. In the web console, open the OAuth consent screen: user type **External**,
   app name `m9s`. Then **Publish app** so it is "In production". While it is
   in "Testing", Google expires refresh tokens after 7 days. (Menu names change
   often; look for the consent screen / Google Auth platform section.)
3. Create an OAuth client of type **Desktop app**. Note its client ID and
   client secret.

### Config

```yaml
accounts:
  - name: gmail-camper
    provider: gmail
    keepassxc: true                    # required for Gmail
    gmail:
      user: someone@gmail.com
      client_id: 1234-abc.apps.googleusercontent.com
    folders:
      badge_poll: 30s                  # how often to ask Gmail what changed (default: 30s)
```

### Credentials

Browser integration must be on (see [KeePassXC setup](#keepassxc-setup)).
Two entries:

| Entry | URL | User name | Password |
|-------|-----|-----------|----------|
| client | `https://accounts.google.com` | the client ID | the client secret |
| token | `https://oauth2.googleapis.com` | your Gmail address | the refresh token |

Create the client entry yourself. The token entry is created by
`m9s login <account>` (below).

### Signing in

```sh
m9s login gmail-camper
```

The browser opens for Google's consent. Google warns that the app is
unverified (it is your own project): choose **Advanced**, then **Go to m9s**.
m9s checks that the signed-in address is `gmail.user` and stores the refresh
token in the token entry in KeePassXC. If it says
`signed in as X, config expects Y`, put X in `gmail.user`.

`m9s logout <account>` revokes the token at Google. Delete the token entry in
KeePassXC yourself. `m9s check` shows whether the account is signed in; if
Google has revoked access, m9s says `sign in again: m9s login <account>`.
Run it in another terminal, then press **S** on that account in m9s: the
retry reads the new token from KeePassXC, so no restart is needed.

While the Google Cloud project's OAuth consent screen is in **Testing**,
Google expires refresh tokens after 7 days, so you have to sign in again every
week. To avoid that, set it to **In production** (APIs & Services → OAuth
consent screen → Audience → Publish app). A personal app does not need
verification; Google only shows the unverified warning at sign-in.

If login says the client entry is not found, set that entry's URL to
`https://accounts.google.com`.

### Using Gmail in m9s

- Gmail **labels** are the folders. Open a label to list its conversations.
- `enter` on a conversation opens it; `tab` / `shift-tab` move between its
  messages and `enter` expands or collapses one.
- `L` opens the label picker (Gmail labels; Outlook categories on a Microsoft account).
- `/` filters the list as you type; `enter` then searches the server
  (Gmail search syntax such as `from:alice has:attachment`); `esc` returns to
  the folder.
- `a` archives the selected conversation: it takes the Inbox label off every
  message in it and keeps their other labels. `m` moves it to another label.
- New mail is checked every 30 seconds (`badge_poll`).

#### Archive and All Mail

Archived mail is not deleted, but without its Inbox label it shows only in
the other labels of it that m9s shows. **All Mail** (after Server Drafts in
the folder list) shows it anyway: the newest 50 conversations of the whole mailbox, archived ones
included, Spam and Trash left out — as All Mail on Gmail web.

- All Mail is listed live from Gmail, not kept in the local copy: it needs a
  connection and has no unread/total counts. Opening it shows the last
  listing at once; m9s lists it again when Gmail reports a change, and the
  open list updates when the new one arrives. When a listing fails, the
  bottom row says why (the log, while the account is not connected).
- To bring a conversation back to the Inbox, select it in All Mail, press
  `m` and choose **INBOX**. From All Mail a move only adds the label you
  choose; the conversation keeps every label it had.
- `u`, `*`, `a`, `d`, `enter` and `/` work in All Mail as in any label; `/`
  then `enter` searches the whole mailbox anyway.
- If you have your own label called "All Mail" and show it (`folders.include`),
  that label is shown instead, once m9s has reached Gmail.

## Microsoft 365 / Outlook.com (Graph API)

An account with `provider: microsoft` talks to Microsoft Graph instead of
IMAP/SMTP: Outlook's real folders (nested, with Outlook's counts), Outlook
categories as tags, and Outlook search on the server. It works for a personal
account (Outlook.com, Hotmail) and for a work or school account whose IT lets
you use your own app. Sign-in is OAuth with your own app registration; there
is no client secret. The refresh token lives in KeePassXC, so
`keepassxc: true` is required.

### Setup (one time)

Register m9s as an app with Microsoft. Microsoft renames these menus often;
look for the nearest match.

1. Sign in at [entra.microsoft.com](https://entra.microsoft.com) and open
   **App registrations**. If a personal account cannot create one, first make
   a free Azure account with it ([azure.microsoft.com/free](https://azure.microsoft.com/free)):
   that creates a directory to hold the app.
2. **New registration**, name `m9s`, supported account types **"Any
   organizational directory and personal Microsoft accounts"** (this cannot be
   changed later).
3. **Authentication**: add the platform **Mobile and desktop applications**
   with the redirect URI `http://localhost`, and set **Allow public client
   flows** to **Yes**. Do not add a "Web" platform: it makes Microsoft ask
   for a client secret.
4. **API permissions** → Microsoft Graph → Delegated: `Mail.ReadWrite`,
   `Mail.Send`, `MailboxSettings.Read` and `User.Read`. A personal account
   approves them itself at the first sign-in.
5. Copy the **Application (client) ID** from the app's Overview page into
   `microsoft.client_id`.

### Config

```yaml
accounts:
  - name: outlook
    provider: microsoft
    keepassxc: true                    # required for Microsoft
    microsoft:
      user: someone@outlook.com        # the address Graph reports for the account
      client_id: 00000000-0000-0000-0000-000000000000
    folders:
      badge_poll: 30s                  # how often each folder is asked what changed (default: 30s)
```

### Credentials

m9s keeps one entry, which `m9s login` creates:

| URL | User name | Password |
|-----|-----------|----------|
| `https://graph.microsoft.com` | `microsoft.user` | the refresh token |

Microsoft hands out a **new refresh token on every use** and lets an unused
one expire after 90 days, so m9s writes the newest one back to that entry:
right after the first refresh of a run, then at most once a day. These saves
run in the background. They never show KeePassXC's unlock window and never
ask for an approval.

#### Setting up KeePassXC for the automatic token update

Do this once, so the background save can write to the entry without you:

1. **Browser integration on**: KeePassXC → **Settings → Browser
   Integration → Enable browser integration** (see
   [KeePassXC setup](#keepassxc-setup)). You do not need to tick a browser.
2. **Updates without a prompt**: in the same settings page, open the
   **Advanced** tab and tick **Never ask before updating credentials**.
   Without it, KeePassXC opens an "update entry?" dialog for every save. If
   nobody answers it within a minute, the save fails and m9s tries again an
   hour later.
3. **m9s approved once**: start m9s (or run `m9s login <account>`) while
   KeePassXC is unlocked, allow the new connection, and name it `m9s`. The
   background save uses that approval and never asks for a new one. If you
   remove m9s under **Database → Database Settings → Browser Integration**,
   saves fail until you approve it again.
4. **Entry access remembered**: the first time m9s reads the token entry,
   KeePassXC may ask whether m9s may access it. Tick **Remember** and allow
   it.
5. **Exactly one matching entry**: keep a single entry with URL
   `https://graph.microsoft.com` and user name `microsoft.user`. Do not rename
   it or change its URL. With two such entries m9s cannot tell which one to
   update, and with none it creates a new one in the default group.
6. **Unlocked now and then**: a save needs the database unlocked. If
   KeePassXC locks itself (**Settings → Security → Lock databases after
   inactivity**) or is closed, m9s keeps working with the token in memory and
   tries again every hour. The next try after you unlock saves it.

To check it works, open the log (`l`) after the first sync: a working save
logs `[<account>] Microsoft sign-in saved to KeePassXC`, and a failed one logs
`Microsoft sign-in not saved to KeePassXC (trying again in an hour): <reason>`.
After 7 days without a save the status bar says
`Microsoft sign-in not saved for N days — unlock KeePassXC`. If nothing is
saved for 90 days, the token in KeePassXC expires and you have to run
`m9s login <account>` again.

### Signing in

```sh
m9s login outlook
```

The browser opens at Microsoft's sign-in; pick the account. m9s checks that
the signed-in address is `microsoft.user` and stores the token. If it says
`signed in as X, config expects Y`, put X in `microsoft.user`.

`m9s logout outlook` tells you where to remove m9s's access
(`account.live.com/consent/Manage` for a personal account,
`myapps.microsoft.com` for a work account); Microsoft has no call to revoke
one app's sign-in, and you delete the KeePassXC entry yourself. `m9s check`
shows whether the account is signed in. After
`sign in again: m9s login <account>`, sign in in another terminal and press
**S** on the account in m9s.

### Using Outlook in m9s

- Outlook's folders are the folders, nested as in Outlook. The special ones
  are called `INBOX`, `Sent`, `Drafts`, `Trash`, `Spam` and `Archive` whatever
  your mailbox calls them in its language. A `/` inside a folder's own name
  is shown as `∕`, so `Projects∕2026` is one folder and `Projects/2026` is
  2026 inside Projects.
- Every row is one message (no conversations). `u` marks it unread, `*`
  flags it, `a` archives it to `folders.archive` if set, else Outlook's
  Archive (`no Archive folder on this account` when there is none), `m`
  moves it to another folder.
- `L` opens the category picker; a message's categories show in the reader
  and preview as `Categories: Work, Travel`. m9s does not create categories:
  make them in Outlook.
- `/` filters the list as you type; `enter` then runs an Outlook search on
  the server (`from:bob subject:"q3 report"`); `esc` returns to the folder.
- Sent mail is filed in Sent Items by Microsoft; a server draft is deleted
  once it is sent.

## Keybindings

### Keys

These work from every pane:

| Key | Action |
|-----|--------|
| `j` / `k` | Navigate down / up in the focused pane |
| `g` / `G` | Jump to the top / bottom of the focused pane |
| `tab` | Cycle focus between panes |
| `[` / `]` | Switch to previous / next account |
| `:` | Command prompt: switch to an account or folder by name; `:help` lists every shortcut |
| `/` | Filter the mail list |
| `enter` | Open the folder under the cursor; on a Gmail conversation, open it in the reader |
| `L` | Labels (Gmail) or categories (Outlook) of the selected message: pick them in a list |
| `c` | Compose new message |
| `r` / `R` | Reply / reply-all to the selected message |
| `f` | Forward the selected message (its text; see below) |
| `d` | Delete the selected message (moves it to Deleted Items; in Deleted Items, see below) |
| `m` | Move the selected message (or conversation) to another folder |
| `space` | Select / unselect the message under the cursor for `m` and `d` (see below) |
| `a` | Archive the selected message (or conversation) |
| `*` | Star / unstar the selected message (or conversation) |
| `u` | Mark the selected message (or conversation) unread |
| `b` | Open the selected message in your web browser, as its sender designed it (see below) |
| `e` | Edit a draft (a Local Drafts entry, or a message in the Drafts folder) |
| `s` | Save the selected message's attachments (see below) |
| `o` | Open one of the selected message's attachments in its default app |
| `S` | Sync folder counts, or reconnect now if the account is disconnected |
| `l` | Toggle log view: `j`/`k` and `g`/`G` scroll; `/` opens the filter row on top of the log and shows only the lines with what you type (time, level and message; a capital letter matches case), `enter` keeps the filter, `esc` drops it; `q`, `esc` or `l` closes the log (`esc` clears a filter first) |
| `?` | Help: every shortcut, grouped by where it works, then this README (same as `:help`). A contents pane on the left lists the sections: `j`/`k` or a click jumps the text there, `tab` moves between the contents and the text; in the text `j`/`k` scroll, `/` searches it as you type (`n`/`N` next/previous match, `esc` clears; a capital letter matches case), `←`/`→` scroll code and tables wider than the window |
| `q` | Quit / back |

When the **Folders** pane is focused:

- `enter` — open the folder under the cursor immediately
- on a **Local Drafts** entry the draft is shown in the preview pane;
  `e` opens it in your editor, `d` deletes it (its copy in the server's
  Drafts folder is moved to Deleted Items); on a folder `d` does nothing

In **Server Drafts** (the server's Drafts folder, shown under that name next
to Local Drafts; `:drafts` still finds it), `e` opens the selected draft in your
editor.

The top bar always shows the keys that apply to the focused pane, using your
own bindings if you changed them. It has room for the most used ones; `?`
lists them all.

### Move, archive, star, forward

- `m` opens a list of the account's folders; `enter` moves the message
  there, `esc` cancels. On Gmail a move swaps the open label for the chosen
  one (from All Mail it only adds the chosen one), and Drafts, Sent and Trash
  are not offered (`d` moves to Trash). On
  Outlook the folders are Outlook's own, nested as in Outlook.
- `a` archives. On IMAP the message moves to the account's archive folder:
  `folders.archive` from the config if set, else the folder the server marks
  `\Archive`, else one called `Archive`. Without one, m9s says so and leaves
  the message where it is. On Gmail, archive takes the message out of the
  Inbox and keeps its other labels. On Outlook it moves to `folders.archive`
  if set, else Outlook's Archive folder.
- `*` stars or unstars (`\Flagged` on IMAP, the Starred label on Gmail, the
  flag on Outlook).
- `f` opens your editor on a forward: `Fwd:` subject, nobody to send it to
  yet, and the original's From/Date/Subject/To/Cc above its text.
  **Attachments are not forwarded yet**: their names are listed in the
  draft and on the status bar.

### Selecting several messages

In the mail list, `space` selects the message under the cursor (and moves
to the next one); `space` again unselects it. A click with `shift` (or
`ctrl` / `alt`) held selects or unselects the clicked message. Selected
messages show `✓` and the title counts them (`MAIL LIST [3 selected]`).

With messages selected, `m` moves all of them to the chosen folder and `d`
asks once to move all of them to the trash. In the trash, `d` asks to delete
them for good; that prompt starts on **Cancel**. On Gmail, conversation rows
are moved or trashed as whole conversations, as with a single `m` or `d`.
`esc` clears the selection; so does opening another folder. While a filter
is on, only the selected messages it shows are acted on. Messages that fail
stay selected, and the status bar says how many.

Terminals keep some modifier clicks for themselves: `cmd`-click is never
passed to programs on macOS, and many terminals (kitty, Ghostty, iTerm2
with some settings) use `shift`-click for their own text selection. Where
that happens, use `space`, or try `ctrl`/`alt`-click.

### Deleting in Deleted Items

In Deleted Items, `d` asks:

- **Delete Forever** — permanently delete the selected message;
- **Delete All Forever** (red) — permanently delete *every* message in
  Deleted Items. A second prompt names how many and starts on **Cancel**, so
  a stray Enter cannot empty the folder;
- **Cancel**.

### `:` command prompt

Type an **account name** or a **folder name** of the current account and press
`enter`. Matching is case-insensitive; a folder matches by its full name
(`Archive/2024`) or its last part (`2024`), and a unique prefix is enough
(`:se` opens `Sent`). The best match is shown dimmed as you type; `tab`
accepts it. If a name is both an account and a folder, the account wins. To
reach a folder of another account, switch account first. `:help` opens a
window listing every shortcut (with your own bindings), as `?` does, with
this README below them (`j`/`k` scroll); `q` or `esc` closes it. `:config` edits the config file (see below). `:q` or `:quit`
quits. `esc` cancels.

### `/` filter

The mail list narrows as you type to messages whose sender (name or address)
or subject contains the text. `enter` keeps the filter, moves to the mail
list and also sends the text to the server as a search (Gmail search syntax on
Gmail, Outlook search on Microsoft, text search on IMAP); `esc` — in the prompt, or later in the mail list —
leaves the search and clears the filter. While you type, the filter applies
only to the messages already loaded (the newest 50). It is dropped when you
open another folder or account.

### Mail list order

The mail list always shows the newest message on top, by date.

### Reading a message in the browser

In the mail list or the preview, `b` opens the selected message in your
default browser: HTML mail with its layout and pictures, plain-text mail as a
simple page. Attached pictures are shown under the message; other attachments
are listed by name.

Like Outlook, the page starts with everything from the internet switched off.
A bar on top says so and has one button, **Load pictures and enable links**:

| | Before the button | After the button |
|---|---|---|
| Pictures the mail carries itself | shown | shown |
| Pictures, style sheets, fonts, media on the sender's server | not fetched | fetched |
| Links | do nothing | open in a new tab |
| Scripts, forms, automatic redirects in the mail | never | never |

What staying off protects you from:

- **Being tracked.** A remote picture (often an invisible 1×1 "pixel") tells
  the sender that you opened the mail, when, how often, from which IP address
  (roughly where you are) and with which browser. For a spammer it also
  proves the address is read by a human.
- **Requests made in your name.** A mail can point a "picture" at any
  address, including devices on your own network (a router, an intranet
  page); the browser would call it just by showing the mail.
- **Content that changes after delivery.** What a remote picture shows is
  decided when you look, not when the mail was sent or scanned.
- **Attacks on the browser.** Nothing the sender controls is downloaded and
  decoded just because you looked at the mail.
- **A slip of the finger.** A phishing link cannot be followed by accident;
  you first have to decide that you trust this mail.

What it does not do: judge the mail for you. Once you press the button the
sender learns you opened it, and a link leads wherever it leads — look at the
address before you click.

The page is a temporary file in a private folder and is deleted when m9s
quits. Only PNG, JPEG, GIF, WebP, AVIF and BMP pictures are shown; anything
else (SVG and HTML attachments included) is listed by name.

### Attachments

A `📎` in the mail list marks a message (or conversation) with files. It shows
as soon as the list loads and is corrected once the message's body is
downloaded; pictures that an HTML mail only shows inside itself don't count.

- `s` saves. With several files a picker lists them with their sizes, plus
  **All files**. They go to `ui.attachment_dir` if set; otherwise m9s asks for
  a folder, starting from the one you used last (or `~/Downloads`). A missing
  folder is created, and an existing file is never replaced: a second
  `report.pdf` is saved as `report (1).pdf`.
- `o` opens one file in its default app (Preview for a PDF, and so on). Files
  that can run programs (`.app`, `.command`, `.sh`, `.pkg`, `.dmg`, `.exe`, …)
  are not opened; save them with `s` instead. Opened files are removed when
  m9s quits.
- On a conversation row, `s` and `o` offer the files of every message in it;
  in the reader, those of the message you are on.
- Names come from the sender, so m9s keeps only the file name itself: no
  folders, no hidden or invisible characters, no leading dot.

### Custom keybindings

Create `~/.config/m9s/keybindings.yaml`:

```yaml
compose: c
reply: r
reply_all: R
forward: f
delete: d
open_browser: b
mark_unread: u
star: "*"
sync: S
archive: a
search_local: /
save_attachments: s
open_attachment: o
move: m
labels: L          # label / category picker
edit: e
command_bar: ":"
mark: " "          # select / unselect for m and d
switch_account_next: "]"
switch_account_prev: "["
back: q
log: l
help: "?"
```

Each value is a single character. Leave blank to keep the default.
`j`/`k`, `g`/`G`, `tab` and `enter` are fixed and cannot be rebound here. An
unknown action name is skipped, and a key given to two actions stays with the
one named later in alphabetical order; both are noted in the log (`l`).

## CLI Commands

| Command | What it does |
|---------|--------------|
| `m9s` | Start the TUI |
| `m9s check [-a <account>]` | Test every account's connection (or one) |
| `m9s login <account>` | Store a password in the Keychain, or sign a Gmail / Microsoft account in |
| `m9s logout <account>` | Remove the Keychain passwords, or sign a Gmail / Microsoft account out |
| `m9s send -a <account> --to … --subject …` | Send a test email |
| `m9s completion <shell>` | Print a shell completion script (`bash`, `zsh`, `fish`, `powershell`) |
| `m9s --version` | Print the version |

Every command takes `-c <path>` / `--config <path>` for another config file,
and `-h` for its help.

### `m9s check`

Tests every enabled account: IMAP and SMTP, or the Gmail / Microsoft
sign-in:

```console
$ m9s check
[personal] IMAP: ✓ connected, 8 folders (password: keepassxc)
[personal] SMTP: ✓ auth OK (password: keepassxc)
[work] IMAP: ✗ no password
    keepassxc: KeePassXC is not running
    password_cmd: password_cmd failed: exit status 1
[work] SMTP: ✓ auth OK (password: plain text)
[gmail-camper] gmail: ✓ signed in as someone@gmail.com (secrets: client keepassxc, token keepassxc)
[outlook] microsoft: ✓ signed in as someone@outlook.com (token: keepassxc)
```

It looks passwords up exactly as m9s does at startup, printing a KeePassXC
wait on stderr instead of the window (Ctrl-C to give up). `-a <account>`
checks one account. For Gmail and Microsoft it can also say
`✗ not signed in` (with what is missing),
`✗ sign in again (run: m9s login <account>)`, or
`✗ signed in as X, config expects Y`.

### `m9s login` / `m9s logout`

What they do depends on the account's provider:

| Provider | `m9s login <account>` | `m9s logout <account>` |
|----------|-----------------------|------------------------|
| IMAP/SMTP | asks for the password, verifies it, stores it in the Keychain (`--imap`, `--smtp`, `--no-verify`) | removes the Keychain entries |
| Gmail | signs in with Google in the browser, stores the refresh token in KeePassXC | revokes the token at Google |
| Microsoft | signs in with Microsoft in the browser, stores the refresh token in KeePassXC | says where to remove m9s's access |

Details: IMAP [Signing in](#signing-in), Gmail [Signing in](#signing-in-1),
Microsoft [Signing in](#signing-in-2).

### `m9s send`

Sends a test email from the command line (useful for verifying a new
account). It goes out over SMTP, or through the Gmail / Microsoft Graph API
for those accounts:

```sh
m9s send --account gmail --to recipient@example.com --subject "Test" --body "Hello"
```

`--account` / `-a` defaults to the first account; `--to` and `--subject` are
required.

## Local Draft Storage

After you close the editor, m9s asks:

- **Send** — send it now;
- **Server Draft** — keep it, and upload it to Server Drafts straight away;
- **Local Draft** — keep it only on this machine; it is never uploaded.

Either way m9s then syncs by itself, as if you had pressed `S`: a Server
Draft is uploaded first, then all folder counts and the open folder's list are
refreshed. If the account is disconnected, the Server Draft is uploaded as
soon as it reconnects.

Either kind of draft is stored locally as an `.eml` file under:

```text
~/.local/share/m9s/drafts/<account-name>/
```

It appears in the **Local Drafts** node at the bottom of the folder tree.
Editing a draft (`e`) asks the same question again, so a Local Draft can
become a Server Draft and the other way round.

- **Editing** (`e`) replaces the draft: the new version is uploaded and the
  old server copy is removed (not moved to Deleted Items — it was replaced,
  not deleted). Editing a message in the server Drafts folder that came from
  one of your local drafts edits that local draft, so there are never two.
- **Sending** a draft removes it locally; its old server copy was already
  replaced when you last edited it.
- **Deleting** (`d` on a Local Drafts entry) removes it locally and moves the
  server copy to Deleted Items, so it can be recovered.

## Roadmap

New providers:

- **MS Teams** (`provider: teams`): channels and chats as folders, messages
  in the mail list, replies from the editor.
- **Telegram** (`provider: telegram`): chats as folders, messages in the mail
  list, replies from the editor.
- **Signal** (`provider: signal`): the same for Signal conversations.

Not implemented yet:

- The full-screen reader opens Gmail conversations only; on an IMAP or
  Outlook message `enter` does nothing yet (the preview shows it).
- Forward does not carry attachments, and compose cannot attach files.
- `J`/`K` (scroll the preview) and `h` (focus left) are in the keymap but do
  nothing yet.

## License

m9s is licensed under the [Apache License 2.0](LICENSE).
