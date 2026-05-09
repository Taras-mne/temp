package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/mail"
	"os"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

type subscribeRequest struct {
	Email string `json:"email"`
}

type apiResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func main() {
	addr := envOr("FOLDY_SUBSCRIBE_ADDR", "127.0.0.1:7043")
	dbPath := envOr("FOLDY_SUBSCRIBE_DB", "subscribers.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS subscribers (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		email      TEXT    NOT NULL UNIQUE COLLATE NOCASE,
		ip         TEXT    NOT NULL,
		created_at TEXT    NOT NULL
	)`); err != nil {
		log.Fatalf("create table: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/subscribe", subscribeHandler(db))

	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		IdleTimeout:  30 * time.Second,
	}
	log.Printf("foldy-subscribe listening on %s, db=%s", addr, dbPath)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func subscribeHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Error: "method not allowed"})
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
		var req subscribeRequest
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid body"})
			return
		}

		email := strings.ToLower(strings.TrimSpace(req.Email))
		if !isValidEmail(email) {
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid email"})
			return
		}

		ip := clientIP(r)
		ts := time.Now().UTC().Format(time.RFC3339)

		_, err := db.ExecContext(r.Context(),
			`INSERT INTO subscribers(email, ip, created_at) VALUES(?, ?, ?)`,
			email, ip, ts,
		)
		if err != nil {
			var sqliteErr *sqlite.Error
			if errors.As(err, &sqliteErr) && sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE {
				writeJSON(w, http.StatusConflict, apiResponse{Error: "already subscribed"})
				return
			}
			log.Printf("insert: %v", err)
			writeJSON(w, http.StatusInternalServerError, apiResponse{Error: "server error"})
			return
		}

		writeJSON(w, http.StatusCreated, apiResponse{OK: true})
	}
}

// isValidEmail does a conservative RFC 5322 parse plus a couple of shape
// checks (single address, has a dot in the domain, length cap).
func isValidEmail(s string) bool {
	if len(s) < 3 || len(s) > 254 {
		return false
	}
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s {
		return false
	}
	at := strings.LastIndex(s, "@")
	if at < 1 || at == len(s)-1 {
		return false
	}
	domain := s[at+1:]
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return false
	}
	return true
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.Index(xff, ","); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeJSON(w http.ResponseWriter, status int, body apiResponse) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
