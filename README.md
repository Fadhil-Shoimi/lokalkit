<p align="center"><img src="assets/logo-mark.svg" width="120" alt="LokalKit"></p>

<h1 align="center">LokalKit</h1>
<p align="center">by Codeslab</p>

Persekitaran pembangunan PHP lokal untuk Windows. Satu klik untuk **Apache + PHP + MySQL + Mailpit**, dengan virtual host automatik (`nama-projek.test`).

![status](https://img.shields.io/badge/status-alpha-orange)

## Ciri-ciri (v0.1)

- Mula/Henti Apache, MySQL/MariaDB dan Mailpit (atau **Mula Semua**)
- **Auto virtual host**: setiap folder dalam `www\` jadi `http://folder.test` (fail hosts dikemas kini automatik, UAC diminta hanya bila perlu)
- Folder `public\` dikesan automatik (Laravel, Symfony)
- Tukar versi PHP. `php.ini` dijana sendiri dengan extension biasa dihidupkan
- MySQL kali pertama disediakan sendiri (`root` tanpa kata laluan)
- Emel PHP `mail()` terus ke Mailpit
- Butang pantas: Web, Database (HeidiSQL/Adminer), Terminal (PHP, Composer, MySQL dalam PATH), folder www
- Cipta projek baru: PHP kosong atau **Laravel** (database dicipta sekali)
- Port boleh ditukar. Semua dalam satu folder, jadi boleh dibawa ke mana-mana (portable)

## Struktur folder

```
C:\LokalKit\
├── LokalKit.exe
├── lokalkit.json            ← tetapan (dicipta automatik)
├── bin\
│   ├── apache\httpd-2.4.x\...\bin\httpd.exe
│   ├── php\php-8.3.x-Win32-vs16-x64\php.exe   (mesti versi TS)
│   ├── mysql\mysql-8.4.x-winx64\bin\mysqld.exe
│   ├── mailpit\mailpit.exe
│   ├── composer\composer.phar
│   ├── heidisql\heidisql.exe   (pilihan)
│   ├── nodejs\node.exe         (pilihan)
│   └── git\cmd\git.exe         (pilihan)
├── etc\     ← config yang dijana (apache, mysql)
├── www\     ← projek anda
├── data\    ← data MySQL & Mailpit
└── logs\
```

Boleh ada beberapa versi PHP dalam `bin\php\`, kemudian pilih versi dari menu atas.

## Cara pasang

1. Buat folder `C:\LokalKit` dan letak `LokalKit.exe` di dalamnya.
2. Buka `LokalKit.exe` sekali. Folder `bin`, `www` dan lain-lain akan dicipta.
3. Isi folder `bin\` dengan binary rasmi (semuanya percuma dan open-source):

| Komponen | Muat turun | Letak di |
|---|---|---|
| Apache (VS17 x64) | https://www.apachelounge.com/download/ | `bin\apache\` |
| PHP **Thread Safe** x64 | https://windows.php.net/download/ | `bin\php\php-8.3.x\` |
| MySQL (ZIP Archive) | https://dev.mysql.com/downloads/mysql/ | `bin\mysql\` |
| Mailpit (windows-amd64) | https://github.com/axllent/mailpit/releases | `bin\mailpit\` |
| Composer | https://getcomposer.org/download/latest-stable/composer.phar | `bin\composer\` |
| HeidiSQL portable (pilihan) | https://www.heidisql.com/download.php | `bin\heidisql\` |
| Adminer (pilihan) | https://www.adminer.org/ | `etc\apps\adminer\index.php` |

> **Dah ada Laragon?** Boleh salin terus folder dalam `C:\laragon\bin\` (apache, php, mysql, mailpit, composer, heidisql). Selepas salin PHP, **padam `php.ini`** dalam folder PHP tu supaya LokalKit jana yang baru (yang lama merujuk ke `C:\laragon`). **Tutup Laragon dulu** supaya port 80/3306 tak bertembung.

4. Tekan **▶ Mula Semua** dan buka `http://localhost`.

Kali pertama Apache dimulakan dengan projek, Windows akan minta kebenaran admin (UAC) untuk kemas kini fail hosts. Ini normal.

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

## Tetapan Apache sendiri

Fail `etc\apache\httpd.conf` dan `etc\apache\sites-enabled\auto.*.conf` **dijana semula** setiap kali Apache dimulakan. Untuk tetapan sendiri:
- `etc\apache\custom.conf`: tetapan global
- `etc\apache\sites-enabled\nama-lain.conf`: vhost manual (nama tidak bermula dengan `auto.`)

## Pembangunan (untuk developer)

Keperluan: Go 1.24+, Node 20+, [Wails v2](https://wails.io).

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

Untuk `wails dev`, set `LOKALKIT_ROOT=C:\LokalKit` supaya ia guna folder sebenar.

Struktur kod:
- `assets/`: logo (SVG). Ikon app dijana dari `build/appicon.png`
- `internal/core/`: enjin (urus proses, jana config Apache/MySQL/php.ini, vhost, fail hosts)
- `app.go`: fungsi yang dipanggil oleh UI
- `frontend/`: UI (HTML/CSS/JS + Vite)

## Pelan seterusnya

- [ ] Ikon di system tray
- [ ] SSL lokal (`https://projek.test`) dengan mkcert
- [ ] Muat turun binary terus dari dalam app
- [ ] Redis, PostgreSQL, Node.js
- [ ] Installer (NSIS)
- [ ] Kongsi projek (tunnel)

---
© 2026 Codeslab
