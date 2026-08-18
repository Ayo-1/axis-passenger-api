# Go Driver Queue API

A Gin-based API that assigns drivers from your Laravel MySQL DB to users via a queue. Each user gets a unique driver. Driver is locked until the user disconnects.

---

## How it works

```
User connects → GET /driver/assign?session_id=xxx
                       ↓
             Finds first free driver (FOR UPDATE SKIP LOCKED)
                       ↓
             Creates row in driver_assignments table
                       ↓
             Streams SSE → sends driver data to frontend
                       ↓
             User disconnects (close tab / navigate away)
                       ↓
             Go detects ctx.Done() → sets released_at on assignment
                       ↓
             Driver is free again for the next user
```

---

## Project structure

```
goapi/
├── main.go                  # Entry point, router setup
├── .env                     # DB credentials and port
├── go.mod                   # Dependencies
├── config/
│   └── db.go                # DB connection + migration
├── models/
│   └── driver.go            # Driver (your Laravel table) + DriverAssignment (new table)
├── handlers/
│   └── driver.go            # SSE assign endpoint + active assignments debug
├── apache-goapi.conf        # Apache2 virtual host config
├── goapi.service            # systemd service
├── setup.sh                 # One-shot deploy script
└── frontend-example.js      # How to consume from JS
```

---

## Setup

### Step 1 — Edit `.env`
```
DB_HOST=127.0.0.1
DB_PORT=3306
DB_USER=your_db_user
DB_PASS=your_db_password
DB_NAME=your_laravel_db
PORT=8081
```

### Step 2 — Check `models/driver.go`
Make sure the `Driver` struct fields match your actual `drivers` table columns.

### Step 3 — Update `apache-goapi.conf`
Replace `api.yourdomain.com` with your actual subdomain.

### Step 4 — Deploy
```bash
chmod +x setup.sh
./setup.sh
```

---

## API Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/health` | Health check |
| GET | `/driver/assign?session_id=xxx` | SSE — assigns and streams a driver |
| GET | `/driver/active` | Lists all currently assigned drivers |

---

## Useful commands on server

```bash
# View live logs
sudo journalctl -u goapi -f

# Restart after code changes
sudo systemctl restart goapi

# Check status
sudo systemctl status goapi
```
