<p align="center"><img src="assets/logo-mark.svg" width="120" alt="LokalKit"></p>

<h1 align="center">LokalKit</h1>
<p align="center">by Codeslab</p>

A local PHP development environment for Windows. **Apache + PHP + MySQL + Mailpit** in one click, with automatic virtual hosts (`project-name.test`) and HTTPS.

![status](https://img.shields.io/badge/status-alpha-orange)

## Features

- Start/stop Apache, MySQL/MariaDB and Mailpit, one at a time or with **Start All**
- **Automatic virtual hosts**: every folder in `www\` becomes `http://folder.test`. The hosts file is updated for you, and Windows asks for admin rights (UAC) only when something changes
- **Automatic HTTPS**: every project also gets `https://folder.test` (and `https://*.folder.test`), signed by LokalKit's own local CA. No mkcert needed
- `public\` folders are detected automatically (Laravel, Symfony)
- Switch PHP versions. `php.ini` is generated for you with the common extensions enabled
- **PHP updates**: a **↑ 8.x.y** button appears next to the PHP menu when windows.php.net has a newer build of your PHP; other PHP branches (e.g. 8.4, 8.5) can be downloaded from the menu. Your `php.ini` is carried over, Apache restarts, and the previous PHP stays installed so you can switch back
- MySQL is initialized on first start (`root`, no password)
- PHP `mail()` goes straight to Mailpit
- **LokalKit Terminal**: opens in Windows Terminal with a LokalKit profile (black & yellow theme, switch icon), a coloured prompt, PHP/Composer/MySQL/Node on PATH and `art` as a shortcut for `php artisan`. The "LokalKit" profile also appears in Windows Terminal's ⌄ menu
- Quick buttons: Web, Database (opens DBeaver with a ready "LokalKit MySQL" connection, else HeidiSQL or Adminer), Terminal (PHP, Composer and MySQL on PATH), www folder
- Create new projects: blank PHP, **Laravel**, or Laravel with the official **React, Vue or Livewire starter kit** (always the latest, from each kit's main branch like the Laravel installer). Composer runs inside LokalKit with progress on the project; the kit's own setup steps run, `.env` is prepared (HTTPS `APP_URL`, MySQL database + migrations when MySQL is running, Mailpit), and the front-end is built with `npm install` + `npm run build`. Projects with missing packages or an unbuilt front-end get a **Repair / Build now** button
- **Database per new project**: choose *Create a MySQL database* (named after the project, editable; MySQL is started if needed; `.env` set up and migrations run, so the starter kits' login/register work right away), *SQLite*, or *No database*
- **System tray**: a switch icon in the taskbar (yellow = services running, grey = stopped). Click to open, right-click to start/stop services and for shortcuts. The X button hides the app to the tray (configurable in Settings)
- **One-click installer**: download Apache, PHP, MySQL, Mailpit, Composer, Adminer and the Visual C++ Runtime from their official sites, with SHA-256 checks where the source publishes them
- **Wide windows** (maximised, 1400px+): an **Activity** panel with the live log stays on the right, and projects and databases are shown in two columns. Smaller windows use the one-column layout with a Logs tab
- Configurable ports. Everything lives in one folder, so the install is portable

## Folder layout

```
C:\LokalKit\
├── LokalKit.exe
├── lokalkit.json            ← settings (created automatically)
├── bin\
│   ├── apache\httpd-2.4.x\...\bin\httpd.exe
│   ├── php\php-8.3.x-Win32-vs16-x64\php.exe   (must be a TS build)
│   ├── mysql\mysql-8.4.x-winx64\bin\mysqld.exe
│   ├── mailpit\mailpit.exe
│   ├── composer\composer.phar
│   ├── heidisql\heidisql.exe   (optional)
│   ├── nodejs\node.exe         (optional)
│   └── git\cmd\git.exe         (optional)
├── etc\     ← generated config (apache, mysql, ssl)
├── www\     ← your projects
├── data\    ← MySQL & Mailpit data
└── logs\
```

You can keep several PHP versions in `bin\php\` and pick one from the menu at the top.

## Installation

1. Create a `C:\LokalKit` folder and put `LokalKit.exe` in it.
2. Run `LokalKit.exe` once. It creates `bin`, `www` and the other folders.
3. Open the **Install** tab (it opens by itself on first run), keep the recommended items ticked and click **Install selected**. LokalKit downloads everything from the official websites into `bin\`.

   Prefer to do it by hand? Put the official binaries in `bin\` yourself:

| Component | Download | Put in |
|---|---|---|
| Apache (VS17 x64) | https://www.apachelounge.com/download/ | `bin\apache\` |
| PHP **Thread Safe** x64 | https://windows.php.net/download/ | `bin\php\php-8.3.x\` |
| MySQL (ZIP Archive) | https://dev.mysql.com/downloads/mysql/ | `bin\mysql\` |
| Mailpit (windows-amd64) | https://github.com/axllent/mailpit/releases | `bin\mailpit\` |
| Composer | https://getcomposer.org/download/latest-stable/composer.phar | `bin\composer\` |
| HeidiSQL portable (optional) | https://www.heidisql.com/download.php | `bin\heidisql\` |
| Adminer (optional) | https://www.adminer.org/ | `etc\apps\adminer\index.php` |

> **Already using Laragon?** You can copy the folders in `C:\laragon\bin\` (apache, php, mysql, mailpit, composer, heidisql) straight across. After copying PHP, **delete its `php.ini`** so LokalKit generates a fresh one (the old one points at `C:\laragon`). **Close Laragon first** so ports 80/443/3306 don't clash.

4. Click **▶ Start All** and open `http://localhost`.

The first time Apache starts with projects, Windows asks for admin rights (UAC) to update the hosts file. This is expected.

## Updates

LokalKit checks GitHub Releases at start (every 12 hours at most) and shows a banner when a newer version exists; **Download** opens the release page. Quit LokalKit (tray → Quit) and replace `LokalKit.exe` with the new one. Turn the check off in **Settings → LokalKit updates**.

> One-click self-updating (download, swap the exe, restart) is implemented in `internal/updater` but switched off: in an unsigned exe that pattern gets the download flagged by antivirus. It can be turned back on once builds are code-signed.

Publishing a release: create a release with a tag such as `v0.2.0` on GitHub. The build workflow stamps that version into the app and attaches `LokalKit.exe` and `LokalKit.exe.sha256`. **The repository (or a separate releases repository set in `internal/updater/updater.go`) must be public** for LokalKit to see releases.

## Databases tab

- Lists the databases in LokalKit's MySQL with their size, number of tables and the projects whose `.env` uses them
- **Create** a database, **Export** a `.sql` backup (saved in `backups\`, restorable under any name), **Import .sql** (or `.sql.gz`) into a database, **Delete** a database
- **Missing databases**: projects whose `.env` points at a database that doesn't exist are listed (and flagged on the project) with **Create** / **Create + migrate**
- **Open DBeaver** with the ready "LokalKit MySQL" connection

## Importing projects from Laragon (or any folder)

**Projects → Import…** finds `C:\laragon\www` automatically (or click **Browse…** for any folder).

- **Projects are linked, not copied**: they stay in their folder with their git history, `vendor` and `node_modules`. LokalKit just serves them at `https://name.localhost` (or `.test`). **Remove** takes a project out of LokalKit without touching its files.
- **Laravel `.env`**: `APP_URL` is changed to the new address, and `DB_PORT`/`MAIL_PORT` follow LokalKit's settings. The original is saved as `.env.before-lokalkit`.
- **Databases**: the panel lists the databases in Laragon's MySQL data folder (`C:\laragon\data\mysql-*`). **Copy selected databases** opens that folder with Laragon's own MySQL on a spare port, exports the chosen databases and imports them into LokalKit's MySQL. Close Laragon first and start LokalKit's MySQL. A database with the same name in LokalKit is replaced. The Laragon data itself is not changed.

## A quiet terminal for older projects

- **PHP "Deprecated" notices are hidden by default** (`error_reporting = E_ALL & ~E_DEPRECATED & ~E_USER_DEPRECATED`). Older packages (e.g. projects from Laragon) running on a newer PHP print many of them for code you didn't write. Turn them back on in **Settings → General**. A custom `error_reporting` you set yourself is never changed
- php.ini settings that the PHP in use has deprecated (e.g. `session.sid_length` on PHP 8.4+) are turned off automatically
- Laravel projects whose `APP_URL` doesn't match their LokalKit address (e.g. `.test` from Laragon) show **Fix APP_URL**; changing the domain in Settings updates `APP_URL` for projects that used the old address

## Project domains: `.test` or `.localhost`

- **`.test`** (default) needs entries in the Windows hosts file. LokalKit adds them for you (Windows asks for admin rights).
- **`.localhost`** needs **no hosts file at all**: Edge, Chrome and Firefox send `anything.localhost` to your own computer by themselves. Pick it in **Settings → Virtual host → Domain**, or click **use .localhost instead** on a project that shows a hosts warning.

If a project shows **"Not in the Windows hosts file yet"** and **Fix now** fails, an antivirus "hosts file protection" feature is the usual cause. Switch to `.localhost`, or allow LokalKit in your antivirus.

## HTTPS (SSL)

When Apache starts with HTTPS on (the default), LokalKit:
1. Creates a **local CA** once, in `etc\ssl\lokalkit-ca.crt`
2. Issues one certificate for all projects (`project.test`, `*.project.test`, `localhost`). It is re-issued automatically when you add projects
3. Asks Windows to **trust** the CA. Click **Yes** in the Windows security dialog (only once, no admin rights needed)

After that, `https://project.test` works without warnings in Chrome and Edge.

- Missed the dialog? **Settings → HTTPS → Trust certificate**
- **Firefox**: if you still see a warning, open `about:config`, set `security.enterprise_roots.enabled` to `true`, then restart Firefox
- Port 443 busy (Skype/VMware/IIS)? Change the **HTTPS port** in Settings. HTTP keeps working either way
- Laravel behind HTTPS: set `APP_URL=https://project.test` in `.env`
- `lokalkit-ca.key` is your CA's private key. **Never share it**

## System tray

- **Click** the switch icon: open the LokalKit window
- **Right-click**: Start/Stop All, start/stop each service, open localhost, Database, Terminal, www folder, Mailpit
- The **X** button only hides the window. To quit completely: right-click the icon → **Quit**
- Running `LokalKit.exe` again while it is in the tray brings the window back
- Don't like it? Turn off "Close button (X) hides LokalKit to the system tray" in **Settings**

> Windows sometimes hides new icons in the `^` overflow menu. Drag the switch icon onto the taskbar to keep it visible.

## Installer sources

| Item | Where it comes from | Verified |
|---|---|---|
| PHP | newest Thread Safe x64 build of the configured branch, from `windows.php.net/downloads/releases/releases.json` | SHA-256 |
| Apache | newest Apache 2.4 Win64 zip linked on `apachelounge.com/download/` | SHA-256 when published |
| MySQL | pinned version from the official MySQL archive | |
| Mailpit | latest GitHub release | |
| Composer | `getcomposer.org` latest stable | SHA-256 |
| Node.js | newest LTS Windows x64 zip from `nodejs.org/dist/index.json` | SHA-256 |
| DBeaver Community (optional) | `dbeaver.io` portable Windows zip (Java included), GitHub release as fallback | |
| Adminer | `adminer.org/latest.php` | |
| Visual C++ Runtime | `aka.ms/vs/17/release/vc_redist.x64.exe` (Microsoft) | |

If a download moves, the URLs can be fixed without a new `LokalKit.exe`: edit [`manifest.json`](manifest.json) in this repository (any field of `internal/installer/sources.go` → `Sources`). The app reads it on each install. This only works while the repository is public.

## Database (Laravel `.env`)

```
DB_CONNECTION=mysql
DB_HOST=127.0.0.1
DB_PORT=3306
DB_USERNAME=root
DB_PASSWORD=

MAIL_MAILER=smtp
MAIL_HOST=127.0.0.1
MAIL_PORT=1025
```

## Custom Apache settings

`etc\apache\httpd.conf` and `etc\apache\sites-enabled\auto.*.conf` are **regenerated** every time Apache starts. For your own settings:
- `etc\apache\custom.conf`: global settings
- `etc\apache\sites-enabled\any-name.conf`: manual vhosts (any name not starting with `auto.`)

## Development

Requirements: Go 1.24+, Node 20+, [Wails v2](https://wails.io).

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@latest
```

```bash
wails dev
```

```bash
wails build -platform windows/amd64
```

```bash
go test ./internal/...
```

For `wails dev`, set `LOKALKIT_ROOT=C:\LokalKit` so it uses a real install folder.

Integration test against a real Apache + PHP (Linux, needs `apache2` and `libapache2-mod-php`):

```bash
LOKALKIT_IT=1 go test -run Integration ./internal/core
```

Code layout:
- `internal/core/`: the engine (process management, Apache/MySQL/php.ini config, vhosts, hosts file, SSL)
- `internal/tray/`: system tray icon and menu
- `internal/installer/`: downloads and installs the binaries
- `app.go`: functions the UI calls
- `frontend/`: the UI (HTML/CSS/JS + Vite)
- `assets/`: logo (SVG). The app icon is generated from `build/appicon.png`

## Roadmap

- [x] System tray icon
- [x] Local SSL (`https://project.test`)
- [x] Download binaries from inside the app
- [ ] Redis, PostgreSQL, Node.js
- [ ] Installer (NSIS)
- [ ] Share projects (tunnel)

---
© 2026 Codeslab
