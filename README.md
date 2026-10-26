# 🎵 FLAC Server

[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go)](https://golang.org)
[![Platform](https://img.shields.io/badge/Platform-Linux%20%7C%20ARM64%20%7C%20x86__64%20%7C%20Windows-blue)](https://github.com)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
[![Memory Footprint](https://img.shields.io/badge/RAM%20Usage-%3C50MB-success)](#kinerja--efisiensi-resource)

**FLAC Server** adalah server pemutar dan pengelola musik *audiophile* berkinerja tinggi yang ditulis dalam bahasa pemrograman **Go**. Dirancang khusus agar sangat ringan (*resource-friendly*) dengan memori aktif di bawah 50MB, server ini dapat dijalankan dengan mulus di perangkat komputer berdaya rendah (*Single Board Computer* / STB Android lawas seperti Amlogic HG680P berbasis Armbian, Raspberry Pi, hingga Mini PC/NAS).

FLAC Server dilengkapi dengan **Web Player modern bertema gelap bergaya Spotify**, sistem lirik karaoke tersinkronisasi (*word-by-word highlight*), efek kanvas latar belakang ambient yang mengalir (*fluid dynamic ambient canvas*), equalizer audio 10-band studio, serta gateway kompatibel **Subsonic API** untuk terhubung ke aplikasi mobile seperti **Flick**, **Symfonium**, atau **DSub**.

---

## ✨ Fitur Utama

### 1. 🎧 Audiophile Bit-Perfect Streaming
- **Lossless & Direct Audio**: Mendukung format audio murni `.flac`, `.mp3`, dan `.wav` tanpa kompresi ulang (*no transcoding/resampling*).
- **Kernel Zero-Copy Sendfile**: Menggunakan syscall `sendfile` dan HTTP Range Requests (206 Partial Content) untuk penyerahan data audio tercepat dari disk ke jaringan dengan beban CPU minimum.
- **Zero-Delay Instant Playback**: Navigasi dan pemutaran lagu seketika (0ms jeda) memanfaatkan in-memory caching pada browser sebelum sinkronisasi antrean di latar belakang.

### 2. 🎨 Spotify-Inspired Web Player UI
- **Desain Modern & Responsif**: Tampilan tema gelap `#121212` elegan dengan tata letak ala Spotify Desktop & Mobile.
- **Navigasi Lengkap**: Jelajahi koleksi musik berdasarkan Artis, Album, Folder, Lagu Favorit, serta riwayat antrean (*Queue Drawer*).
- **Smart Running Marquee**: Judul lagu dan nama artis yang panjang digulirkan secara halus (*seamless loop*) dengan gradasi pudar (*fade edge*), tanpa terpotong.
- **Pencarian Cepat**: Filter pencarian real-time instan untuk ribuan lagu berdasarkan judul, artis, maupun album.

### 3. 🎤 Mesin Lirik Tersinkronisasi Canggih (LRC Karaoke Engine)
- **Multi-Style Lirik**:
  - **Solo Mode**: Menampilkan satu baris lirik aktif di tengah layar secara dramatis.
  - **Word-by-Word Highlight**: Pada mode Solo, lirik disorot kata per kata secara proporsional. Kata yang telah dinyanyikan **tetap menyala terang** hingga baris selesai dinyanyikan.
  - **Smart Unsynced Fallback**: Otomatis beralih ke mode **Classic** jika lagu tidak memiliki timestamp sinkronisasi (*plain text*), sehingga lirik tetap dapat dibaca penuh.
  - **Pilihan Style Lain**: Tersedia mode *Standard*, *Neon Glow*, *Boxed*, dan *Floating*.
- **Performa 60 FPS Ringan**: Menggunakan teknik *State Caching* pada DOM, sehingga manipulasi elemen hanya terjadi saat transisi kata tanpa membebani browser maupun STB.

### 4. 🌌 Fluid Dynamic Ambient Canvas
- **Pewarnaan Adaptif Cover Album**: Mengekstrak warna dominan dari sampul album secara dinamis.
- **Animasi Aliran Lembut**: Menghasilkan efek gradien cairan pendaran cahaya (*liquid fluid ambient*) di latar belakang tampilan layar penuh (*Fullscreen*) dengan animasi rotasi melayang yang santai dan memanjakan mata.

### 5. 🎛️ Studio-Grade Web Audio DSP & Equalizer
- **10-Band Graphic/Parametric EQ**: Atur frekuensi audio secara presisi (32Hz, 64Hz, 125Hz, 250Hz, 500Hz, 1kHz, 2kHz, 4kHz, 8kHz, 16kHz).
- **Preset Bawaan**: Bass Boost, Acoustic, Electronic, Rock, Vocal Enhancer, Flat, dan Custom Preset.
- **Spatializer & Reverb**: Pengalaman suara spasial 3D yang dapat disesuaikan.
- **Smooth Fading & Crossfade**: Transisi antar trek bebas suara 'klik' atau *pop* melalui *fade-in/fade-out* audio otomatis.

### 6. 🏷️ Metadata & Cover Art Studio
- **Integrasi iTunes Search API**: Otomatis mencari sampul album resolusi tinggi dari Apple iTunes API dengan pembersih query cerdas (*smart query sanitizer*).
- **Manual Cover Search UI**: Modal pencarian manual fleksibel untuk memilih cover terbaik langsung dari UI web.
- **Editor Tag Terintegrasi**: Edit metadata lagu (Judul, Artis, Album, Tahun, Genre) secara langsung dari peramban.

### 7. 📱 Subsonic API Gateway
- Kompatibel dengan protokol **Subsonic / OpenSubsonic API** (`/rest/*`).
- Hubungkan pemutar musik favorit Anda di ponsel (misalnya **Flick**, **Symfonium**, **Audinaut**, **DSub**) dan nikmati koleksi FLAC Anda di mana pun melalui koneksi lokal atau Cloudflare Tunnel.

---

## 🏗️ Arsitektur Proyek

```
flac-server/
├── cmd/
│   └── server/
│       └── main.go           # Titik masuk utama aplikasi (HTTP server & lifecycle)
├── internal/
│   ├── api/                  # Handler REST API, Subsonic endpoints, & Web Router
│   │   ├── assets/           # Frontend SPA (HTML5, Vanilla JS, CSS3 Modern)
│   │   │   └── index.html    # Single Page App lengkap (Spotify UI, Player, EQ)
│   │   ├── dto.go            # Data Transfer Objects & format respons JSON
│   │   ├── scan.go           # Endpoint pemindaian pustaka & metadata
│   │   ├── subsonic.go       # Kompatibilitas Subsonic API
│   │   └── write.go          # Modifikasi metadata lagu & upload file
│   ├── config/               # Parser konfigurasi YAML
│   ├── db/                   # Lapisan database SQLite (Pure Go ModernC)
│   │   ├── db.go             # Koneksi database & inisialisasi PRAGMA
│   │   ├── queries.go        # Kueri SQL teroptimasi & indeks pencarian
│   │   └── schema.sql        # Skema tabel lagu, artis, album, dan favorit
│   ├── meta/                 # Parser tag audio VorbisComment, ID3v2, & lirik LRC
│   ├── paths/                # Utilitas manajemen path file & sanitasi keamanan
│   ├── scanner/              # Background directory scanner & ekstraktor cover art
│   └── streamer/             # Engine streaming zero-copy sendfile & chunking
├── Dockerfile                # Docker build multi-stage (ukuran image super kecil)
├── docker-compose.yml        # Konfigurasi orkestrasi Docker
├── Makefile                  # Perintah cepat build, test, & clean
├── config.example.yaml       # Template konfigurasi server
└── go.mod                    # Modul Go & dependensi
```

---

## ⚡ Kinerja & Efisiensi Resource

FLAC Server dirancang dari awal untuk berjalan di lingkungan dengan spesifikasi perangkat keras terbatas:
- **Bahasa**: Pure Go 1.22+ (Tanpa dependensi CGO, menggunakan `modernc.org/sqlite`).
- **Konsumsi Memori (RAM)**:
  - Kondisi Diam (*Idle*): ~25 MB - 35 MB
  - Saat Streaming FLAC Bit-Perfect: ~40 MB - 55 MB
- **Target Perangkat**:
  - STB Amlogic S905X / HG680P / B860H (Armbian Linux)
  - Raspberry Pi 3 / 4 / 5 / Zero 2 W
  - Router OpenWrt dengan storage USB
  - Mini PC / Home Server / NAS / VPS

---

## 🚀 Panduan Instalasi & Menjalankan

### Cara 1: Menggunakan Binary Go (Rekomendasi untuk Linux / Armbian / Windows)

1. **Clone Repositori**:
   ```bash
   git clone https://github.com/username/flac-server.git
   cd flac-server
   ```

2. **Salin & Sesuaikan Konfigurasi**:
   ```bash
   cp config.example.yaml config.yaml
   nano config.yaml
   ```
   *Atur direktori musik (`library.music_root`) dan direktori cache cover art (`library.art_cache_dir`).*

3. **Kompilasi & Jalankan**:
   ```bash
   # Kompilasi binary
   go build -o server ./cmd/server

   # Jalankan server
   ./server -config config.yaml
   ```

4. Buka peramban Anda di `http://<IP-SERVER>:8082/web/`.

---

### Cara 2: Menjalankan via Docker / Docker Compose

1. **Jalankan dengan Docker Compose**:
   ```bash
   docker-compose up -d --build
   ```

2. **Lihat Log**:
   ```bash
   docker-compose logs -f
   ```

---

### Cara 3: Menjalankan sebagai Systemd Service (Pada STB Armbian HG680P)

Buat file service di `/etc/systemd/system/flac-server.service`:

```ini
[Unit]
Description=FLAC Audio Streaming Server
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/mnt/hdd/AppData/flac-server
ExecStart=/mnt/hdd/AppData/flac-server/server -config /mnt/hdd/AppData/flac-server/config.yaml
Restart=always
RestartSec=5
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
```

Aktifkan dan jalankan service:
```bash
sudo systemctl daemon-reload
sudo systemctl enable --now flac-server
sudo systemctl status flac-server
```

---

## ⚙️ Konfigurasi (`config.yaml`)

| Parameter | Default | Keterangan |
| :--- | :--- | :--- |
| `server.port` | `8082` | Port HTTP server |
| `library.music_root` | `/mnt/hdd/music` | Path folder penyimpanan musik Anda |
| `library.scan_interval` | `300` | Frekuensi pemindaian file baru secara otomatis (detik) |
| `library.extensions` | `[.flac, .mp3, .wav]` | Ekstensi file audio yang didukung |
| `streaming.sendfile` | `true` | Mengaktifkan akselerasi transmisi file zero-copy kernel |
| `auth.enabled` | `true` | Mengaktifkan autentikasi login pengguna |
| `auth.session_ttl` | `24h` | Durasi sesi login aktif |

---

## 📱 Menghubungkan ke Client Subsonic

Untuk mendengarkan lagu di smartphone Android/iOS menggunakan aplikasi pemutar musik Subsonic (seperti **Flick** atau **Symfonium**):
1. **Server URL**: `http://<IP-SERVER>:8082` (atau domain HTTPS Cloudflare Tunnel Anda).
2. **Username**: Username yang Anda daftarkan di `config.yaml`.
3. **Password**: Password akun Anda.

---

## 📄 Lisensi

Proyek ini dilisensikan di bawah lisensi [MIT License](LICENSE) - bebas digunakan, dikembangkan, dan didistribusikan untuk keperluan pribadi maupun komunitas.
