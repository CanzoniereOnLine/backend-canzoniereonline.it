package main

import (
	"archive/zip"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/smtp"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// ════════════════════════════════════════════════════════════════════════════
// Config
// ════════════════════════════════════════════════════════════════════════════

// S3Config raggruppa la configurazione per l'object storage S3-compatible.
// Attiva solo se S3_ENABLED=true; altrimenti il backend funziona come prima.
// Il bucket può essere privato: tutte le operazioni usano AWS Signature V4 server-side.
// Compatibile con qualsiasi provider S3-compatible: Cloudflare R2, DigitalOcean Spaces,
// AWS S3, MinIO, ecc. — basta configurare S3_ENDPOINT correttamente.
type S3Config struct {
	PublicEndpoint string
	Enabled        bool
	Endpoint       string // es. https://<account>.r2.cloudflarestorage.com (R2) o https://fra1.digitaloceanspaces.com
	Bucket         string
	AccessKey      string
	SecretKey      string
	Region         string
	RedirectTTL    int // durata in secondi dei presigned URL per /s3/... (default 3600)
}

type Config struct {
	DBPath              string
	DBReleaseURL        string // URL repo GitHub o asset diretto (.db) su GitHub Releases
	DBUpdateIntervalSec int    // intervallo in secondi tra i check di aggiornamento (default 21600 = 6h)
	TemplatePath        string
	Port                string
	SMTPHost            string
	SMTPPort            string
	SMTPUser            string
	SMTPPass            string
	SMTPFrom            string
	LatexEngine         string // "lualatex" (default) o "pdflatex" (sperimentale)
	S3                  S3Config
	CleanFrom           int
	CleanTo             int
}

func loadConfig() Config {
	s3Enabled := strings.ToLower(getEnv("S3_ENABLED", "false")) == "true"
	return Config{
		DBPath:       getEnv("DB_PATH", "songs.db"),
		DBReleaseURL: getEnv("DB_RELEASE_URL", ""),
		DBUpdateIntervalSec: func() int {
			if v := getEnv("DB_UPDATE_INTERVAL_SEC", ""); v != "" {
				var n int
				fmt.Sscanf(v, "%d", &n)
				if n > 0 {
					return n
				}
			}
			return 21600 // default: 6 ore
		}(),
		TemplatePath: getEnv("TEMPLATE_PATH", "template"),
		Port:         getEnv("PORT", "8080"),
		SMTPHost:     getEnv("SMTP_HOST", "smtp.gmail.com"),
		SMTPPort:     getEnv("SMTP_PORT", "587"),
		SMTPUser:     getEnv("SMTP_USER", ""),
		SMTPPass:     getEnv("SMTP_PASS", ""),
		SMTPFrom:     getEnv("SMTP_FROM", ""),
		LatexEngine:  getEnv("LATEX_ENGINE", "lualatex"),
		CleanFrom: func() int {
			if v := getEnv("CLEAN_FROM", ""); v != "" {
				var n int
				fmt.Sscanf(v, "%d", &n)
				if n > 0 {
					return n
				}
			}
			return -120 // default: 6 ore
		}(),
		CleanTo: func() int {
			if v := getEnv("CLEAN_TO", ""); v != "" {
				var n int
				fmt.Sscanf(v, "%d", &n)
				if n > 0 {
					return n
				}
			}
			return -31 // default: 6 ore
		}(),
		S3: S3Config{
			Enabled:        s3Enabled,
			PublicEndpoint: getEnv("S3_PUBLIC_ENDPOINT", ""),
			Endpoint:       getEnv("S3_ENDPOINT", ""),
			Bucket:         getEnv("S3_BUCKET", ""),
			AccessKey:      getEnv("S3_ACCESS_KEY", ""),
			SecretKey:      getEnv("S3_SECRET_KEY", ""),
			Region:         getEnv("S3_REGION", "auto"), // "auto" è il valore corretto per Cloudflare R2
			RedirectTTL: func() int {
				if v := getEnv("S3_REDIRECT_TTL_SECONDS", ""); v != "" {
					var n int
					fmt.Sscanf(v, "%d", &n)
					if n > 0 {
						return n
					}
				}
				return 3600 // default: 1 ora
			}(),
		},
	}
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ════════════════════════════════════════════════════════════════════════════
// Models
// ════════════════════════════════════════════════════════════════════════════

type Song struct {
	Identificatore string `json:"identificatore"`
	Titolo         string `json:"titolo"`
	Autore         string `json:"autore,omitempty"`
	Album          string `json:"album,omitempty"`
	Tonalita       string `json:"tonalita,omitempty"`
	Famiglia       string `json:"famiglia,omitempty"`
	Gruppo         string `json:"gruppo,omitempty"`
	Momenti        string `json:"momenti,omitempty"`
	DataRevisione  string `json:"data_revisione,omitempty"`
	Trascrittore   string `json:"trascrittore,omitempty"`
	TestoCompleto  string `json:"testo_completo,omitempty"`
	TestoSemplice  string `json:"testo_semplice,omitempty"`
	PdfTestoURL    string `json:"pdf_testo_url,omitempty"`
	PdfAccordiURL  string `json:"pdf_accordi_url,omitempty"`
}

type SongSummary struct {
	Identificatore string `json:"identificatore"`
	Titolo         string `json:"titolo"`
	Autore         string `json:"autore,omitempty"`
	Album          string `json:"album,omitempty"`
	Tonalita       string `json:"tonalita,omitempty"`
	Famiglia       string `json:"famiglia,omitempty"`
	Momenti        string `json:"momenti,omitempty"`
	PdfTestoURL    string `json:"pdf_testo_url,omitempty"`
	PdfAccordiURL  string `json:"pdf_accordi_url,omitempty"`
}

type SongEntry struct {
	Identificatore string `json:"identificatore"`
	Semitoni       int    `json:"semitoni"`
}

type CanzonieRequest struct {
	Titoletto   string      `json:"titoletto"`
	Titolo      string      `json:"titolo"`
	Sottotitolo string      `json:"sottotitolo"`
	NumColonne  int         `json:"num_colonne"`
	Formato     string      `json:"formato"`
	Accordi     string      `json:"accordi"`
	Font        string      `json:"font"`
	MinorSign   string      `json:"minorsign"`
	Indici      []string    `json:"indici"`
	Copertina   string      `json:"copertina"`
	Email       string      `json:"email"`
	Canzoni     []SongEntry `json:"canzoni"`
}

// ════════════════════════════════════════════════════════════════════════════
// Shared helpers
// ════════════════════════════════════════════════════════════════════════════

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func nullStr(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}

// ════════════════════════════════════════════════════════════════════════════
// S3 — client minimale con AWS Signature V4
// Implementato senza dipendenze esterne: usa solo la stdlib.
// Supporta: PutObject (application/json, application/zip, application/pdf,
// application/octet-stream).
// ════════════════════════════════════════════════════════════════════════════

// JobStatus rappresenta lo stato di avanzamento di una compilazione.
type JobStatus struct {
	JobID     string   `json:"job_id"`
	Status    string   `json:"status"`  // queued | fetching_songs | compiling | packaging | uploading | done | error
	Message   string   `json:"message"` // descrizione leggibile dello step corrente
	Error     string   `json:"error,omitempty"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
	Files     []S3File `json:"files,omitempty"` // popolato quando status=done
}

// S3File descrive un file caricato sullo Space.
type S3File struct {
	Name      string `json:"name"`
	Key       string `json:"key"` // path completo su S3
	URL       string `json:"url,omitempty"`
	PUBLICURL string `json:"public_url,omitempty"` // URL pubblico (presigned) se il bucket è privato
}

// s3JobPrefix restituisce il prefisso S3 per un job, organizzato per
// anno/mese/giorno: canzoniere/<YYYY>/<MM>/<DD>/<jobID>/
//
// La data non è un parametro separato: viene ricavata direttamente dai
// primi 8 caratteri del jobID, che newJobID() genera nel formato
// "YYYYMMDDHHmm" + 64 caratteri hex. Questo significa che tutti i punti
// del codice che già chiamano s3JobPrefix/s3Key (redirect, download zip,
// status, upload) ottengono il nuovo layout automaticamente, senza bisogno
// di modifiche alle route né di alcun cambiamento visibile ai client: il
// jobID resta l'unico identificatore che l'API espone e accetta.
//
// Se il jobID non rispetta il formato atteso (troppo corto, o non generato
// da newJobID) si usa un fallback piatto "canzoniere/<jobID>/" per non
// rompere nulla in casi anomali.
func s3JobPrefix(jobID string) string {
	if len(jobID) >= 8 {
		year, month, day := jobID[0:4], jobID[4:6], jobID[6:8]
		if isDigits(year) && isDigits(month) && isDigits(day) {
			return fmt.Sprintf("canzoniere/%s/%s/%s/%s/", year, month, day, jobID)
		}
	}
	return fmt.Sprintf("canzoniere/%s/", jobID) // fallback per jobID anomali/legacy
}

// isDigits verifica che una stringa contenga solo cifre (usata per validare
// la parte di data estratta dal jobID prima di usarla in un path S3).
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// s3Key restituisce la chiave S3 completa per un file in un job.
func s3Key(jobID, filename string) string {
	return s3JobPrefix(jobID) + filename
}

// sigV4Key calcola la signing key per AWS Signature V4.
func sigV4Key(secretKey, dateStamp, region, service string) []byte {
	mac := func(key, data []byte) []byte {
		h := hmac.New(sha256.New, key)
		h.Write(data)
		return h.Sum(nil)
	}
	kDate := mac([]byte("AWS4"+secretKey), []byte(dateStamp))
	kRegion := mac(kDate, []byte(region))
	kService := mac(kRegion, []byte(service))
	kSigning := mac(kService, []byte("aws4_request"))
	return kSigning
}

// sha256Hex calcola SHA-256 hex di un payload.
func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// s3PutObject carica data su S3 con AWS Signature V4.
// contentType: es. "application/json", "application/pdf", "application/zip".
func s3PutObject(cfg S3Config, key string, data []byte, contentType string) error {
	if !cfg.Enabled {
		return nil
	}

	now := time.Now().UTC()
	dateStamp := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")

	// Costruisce l'URL: endpoint/bucket/key
	// DigitalOcean Spaces: https://<region>.digitaloceanspaces.com/<bucket>/<key>
	endpoint := strings.TrimRight(cfg.Endpoint, "/")
	rawURL := fmt.Sprintf("%s/%s/%s", endpoint, cfg.Bucket, key)
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("s3 url parse: %w", err)
	}
	host := u.Host
	path := u.Path

	payloadHash := sha256Hex(data)

	// Canonical request
	canonicalHeaders := fmt.Sprintf("content-type:%s\nhost:%s\nx-amz-content-sha256:%s\nx-amz-date:%s\n",
		contentType, host, payloadHash, amzDate)
	signedHeaders := "content-type;host;x-amz-content-sha256;x-amz-date"
	canonicalRequest := strings.Join([]string{
		"PUT",
		path,
		"", // query string vuota
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	// String to sign
	credentialScope := fmt.Sprintf("%s/%s/s3/aws4_request", dateStamp, cfg.Region)
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	// Firma
	signingKey := sigV4Key(cfg.SecretKey, dateStamp, cfg.Region, "s3")
	mac := hmac.New(sha256.New, signingKey)
	mac.Write([]byte(stringToSign))
	signature := hex.EncodeToString(mac.Sum(nil))

	authHeader := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s,SignedHeaders=%s,Signature=%s",
		cfg.AccessKey, credentialScope, signedHeaders, signature)

	req, err := http.NewRequest(http.MethodPut, rawURL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("s3 new request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Host", host)
	req.Header.Set("X-Amz-Content-SHA256", payloadHash)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("Authorization", authHeader)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("s3 put %s: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("s3 put %s: HTTP %d — %s", key, resp.StatusCode, string(body))
	}
	return nil
}

// s3ObjectURL restituisce l'URL interno (endpoint/bucket/key) di una chiave S3.
// Usato per le richieste firmate server-side — non è un URL pubblico.
func s3ObjectURL(cfg S3Config, key string) string {
	endpoint := strings.TrimRight(cfg.Endpoint, "/")
	return fmt.Sprintf("%s/%s/%s", endpoint, cfg.Bucket, key)
}

func s3ObjectPublicURL(cfg S3Config, key string) string {
	endpoint := strings.TrimRight(cfg.PublicEndpoint, "/")
	return fmt.Sprintf("%s/%s/%s", endpoint, cfg.Bucket, key)
}

// s3GetObject scarica un oggetto da S3 con AWS Signature V4 (GET firmato server-side).
// Il bucket può essere privato: la firma nell'header Authorization autorizza la lettura.
// Restituisce il corpo della risposta; il chiamante è responsabile della chiusura.
func s3GetObject(cfg S3Config, key string) (io.ReadCloser, error) {
	now := time.Now().UTC()
	dateStamp := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")

	rawURL := s3ObjectURL(cfg, key)
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("s3 url parse: %w", err)
	}
	host := u.Host
	path := u.Path

	// Per GET il payload è vuoto; l'hash è quello del corpo vuoto.
	emptyHash := sha256Hex([]byte{})

	canonicalHeaders := fmt.Sprintf("host:%s\nx-amz-content-sha256:%s\nx-amz-date:%s\n",
		host, emptyHash, amzDate)
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalRequest := strings.Join([]string{
		"GET",
		path,
		"", // query string vuota
		canonicalHeaders,
		signedHeaders,
		emptyHash,
	}, "\n")

	credentialScope := fmt.Sprintf("%s/%s/s3/aws4_request", dateStamp, cfg.Region)
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	signingKey := sigV4Key(cfg.SecretKey, dateStamp, cfg.Region, "s3")
	mac := hmac.New(sha256.New, signingKey)
	mac.Write([]byte(stringToSign))
	signature := hex.EncodeToString(mac.Sum(nil))

	authHeader := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s,SignedHeaders=%s,Signature=%s",
		cfg.AccessKey, credentialScope, signedHeaders, signature)

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("s3 get request: %w", err)
	}
	req.Header.Set("Host", host)
	req.Header.Set("X-Amz-Content-SHA256", emptyHash)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("Authorization", authHeader)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("s3 get %s: %w", key, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, fmt.Errorf("s3: chiave non trovata: %s", key)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		return nil, fmt.Errorf("s3 get %s: HTTP %d — %s", key, resp.StatusCode, string(body))
	}
	return resp.Body, nil
}

// s3PutJSON serializza v come JSON e lo carica su S3.
func s3PutJSON(cfg S3Config, key string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s3PutObject(cfg, key, data, "application/json")
}

// s3UpdateStatus aggiorna il file status.json per un job.
// Non interrompe il flusso se l'upload fallisce: logga solo un warning.
func s3UpdateStatus(cfg S3Config, status *JobStatus) {
	if !cfg.Enabled {
		return
	}
	status.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	key := s3Key(status.JobID, "status.json")
	if err := s3PutJSON(cfg, key, status); err != nil {
		log.Printf("WARN s3 status update (%s → %s): %v", status.JobID, status.Status, err)
	}
}

// s3ListObjects elenca le chiavi sotto un prefix nel bucket S3.
// Usa l'API ListObjectsV2 (query string ?list-type=2&prefix=...).
// La risposta XML viene parsata senza dipendenze esterne.
func s3ListObjects(cfg S3Config, prefix string) ([]string, error) {
	now := time.Now().UTC()
	dateStamp := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")

	endpoint := strings.TrimRight(cfg.Endpoint, "/")

	// Cloudflare R2 e la maggior parte degli S3-compatible usano path-style:
	// https://<endpoint>/<bucket>?list-type=2&prefix=...
	qs := fmt.Sprintf("list-type=2&prefix=%s", url.QueryEscape(prefix))
	rawURL := fmt.Sprintf("%s/%s?%s", endpoint, cfg.Bucket, qs)
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("s3 list url: %w", err)
	}
	host := u.Host
	path := u.Path

	emptyHash := sha256Hex([]byte{})

	// La query string canonica deve essere URL-encoded e ordinata per chiave.
	canonicalQS := qs // già ordinata (due parametri in ordine alfabetico)
	canonicalHeaders := fmt.Sprintf("host:%s\nx-amz-content-sha256:%s\nx-amz-date:%s\n",
		host, emptyHash, amzDate)
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalRequest := strings.Join([]string{
		"GET",
		path,
		canonicalQS,
		canonicalHeaders,
		signedHeaders,
		emptyHash,
	}, "\n")

	credentialScope := fmt.Sprintf("%s/%s/s3/aws4_request", dateStamp, cfg.Region)
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	signingKey := sigV4Key(cfg.SecretKey, dateStamp, cfg.Region, "s3")
	mac := hmac.New(sha256.New, signingKey)
	mac.Write([]byte(stringToSign))
	signature := hex.EncodeToString(mac.Sum(nil))

	authHeader := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s,SignedHeaders=%s,Signature=%s",
		cfg.AccessKey, credentialScope, signedHeaders, signature)

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("s3 list request: %w", err)
	}
	req.Header.Set("Host", host)
	req.Header.Set("X-Amz-Content-SHA256", emptyHash)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("Authorization", authHeader)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("s3 list: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("s3 list HTTP %d: %s", resp.StatusCode, string(body))
	}

	// Parsing XML minimale: estrae i valori di tutti i tag <Key>...</Key>
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("s3 list read: %w", err)
	}
	var keys []string
	for _, chunk := range strings.Split(string(body), "<Key>") {
		if i := strings.Index(chunk, "</Key>"); i > 0 {
			keys = append(keys, chunk[:i])
		}
	}
	return keys, nil
}

// s3PresignedGetURL genera un URL presigned GET valido per `ttl` secondi.
// Permette un redirect diretto al file su S3 senza esporre le credenziali:
// il client riceve solo un URL temporaneo firmato.
func s3PresignedGetURL(cfg S3Config, key string, ttl int) (string, error) {
	now := time.Now().UTC()
	dateStamp := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")

	endpoint := strings.TrimRight(cfg.PublicEndpoint, "/")
	rawURL := fmt.Sprintf("%s/%s/%s", endpoint, cfg.Bucket, key)
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("s3 presign url: %w", err)
	}
	host := u.Host
	path := u.Path

	credentialScope := fmt.Sprintf("%s/%s/s3/aws4_request", dateStamp, cfg.Region)
	credential := fmt.Sprintf("%s/%s", cfg.AccessKey, credentialScope)

	// Canonical query string per presigned URL (parametri in ordine alfabetico)
	qs := fmt.Sprintf(
		"X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=%s&X-Amz-Date=%s&X-Amz-Expires=%d&X-Amz-SignedHeaders=host",
		url.QueryEscape(credential), amzDate, ttl,
	)

	canonicalHeaders := fmt.Sprintf("host:%s\n", host)
	signedHeaders := "host"
	// Per presigned URL il payload hash è la stringa letterale "UNSIGNED-PAYLOAD"
	canonicalRequest := strings.Join([]string{
		"GET",
		path,
		qs,
		canonicalHeaders,
		signedHeaders,
		"UNSIGNED-PAYLOAD",
	}, "\n")

	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	signingKey := sigV4Key(cfg.SecretKey, dateStamp, cfg.Region, "s3")
	mac := hmac.New(sha256.New, signingKey)
	mac.Write([]byte(stringToSign))
	signature := hex.EncodeToString(mac.Sum(nil))

	presigned := fmt.Sprintf("%s?%s&X-Amz-Signature=%s", rawURL, qs, signature)
	return presigned, nil
}

// s3DeleteObject elimina un oggetto da S3 usando AWS Signature V4 (DELETE firmato server-side).
func s3DeleteObject(cfg S3Config, key string) error {
	if !cfg.Enabled {
		return nil
	}

	now := time.Now().UTC()
	dateStamp := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")

	rawURL := s3ObjectURL(cfg, key)
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("s3 url parse: %w", err)
	}
	host := u.Host
	path := u.Path

	// Per i DELETE il payload è vuoto; l'hash è quello del corpo vuoto.
	emptyHash := sha256Hex([]byte{})

	canonicalHeaders := fmt.Sprintf("host:%s\nx-amz-content-sha256:%s\nx-amz-date:%s\n",
		host, emptyHash, amzDate)
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalRequest := strings.Join([]string{
		"DELETE",
		path,
		"", // query string vuota
		canonicalHeaders,
		signedHeaders,
		emptyHash,
	}, "\n")

	credentialScope := fmt.Sprintf("%s/%s/s3/aws4_request", dateStamp, cfg.Region)
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	signingKey := sigV4Key(cfg.SecretKey, dateStamp, cfg.Region, "s3")
	mac := hmac.New(sha256.New, signingKey)
	mac.Write([]byte(stringToSign))
	signature := hex.EncodeToString(mac.Sum(nil))

	authHeader := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s,SignedHeaders=%s,Signature=%s",
		cfg.AccessKey, credentialScope, signedHeaders, signature)

	req, err := http.NewRequest(http.MethodDelete, rawURL, nil)
	if err != nil {
		return fmt.Errorf("s3 delete request: %w", err)
	}
	req.Header.Set("Host", host)
	req.Header.Set("X-Amz-Content-SHA256", emptyHash)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("Authorization", authHeader)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("s3 delete %s: %w", key, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("s3 delete %s: HTTP %d — %s", key, resp.StatusCode, string(body))
	}
	return nil
}

// SvuotaCartelleVecchie individua le cartelle dei job (jobID) create tra 30 e
// 120 giorni fa e ne svuota tutti i file, tenendo solo lo "status.json"
// (che contiene i dettagli/richiesta di creazione).
//
// A differenza della versione precedente, NON elenca l'intero prefisso
// "canzoniere/" (che cresce senza limiti nel tempo e include anche i job
// recenti che non ci interessano) e NON scarica lo status.json di ogni job
// per leggerne la data: la data di creazione è già codificata nel path
// stesso (canzoniere/<YYYY>/<MM>/<DD>/<jobID>/...), quindi basta elencare
// un giorno alla volta, solo per i ~91 giorni della finestra [-120, -30].
// Il costo della routine resta così costante nel tempo, indipendentemente
// da quanti job si sono accumulati nel bucket.
//
// NOTA: le cartelle create con il vecchio schema piatto (canzoniere/<jobID>/,
// da prima dell'introduzione del layout per data) non hanno un path
// enumerabile per giorno e non vengono più raggiunte da questa routine.
// Se ci sono ancora cartelle di quel tipo da smaltire serve un cleanup
// una tantum separato.
func SvuotaCartelleVecchie(cfg S3Config, CleanFrom int, CleanTo int) {
	if !cfg.Enabled {
		return
	}

	log.Println("[Pulizia S3] Avvio scansione per rimozione canzonieri obsoleti...")

	now := time.Now().UTC()
	limiteTrentaGiorni := now.AddDate(0, 0, CleanTo)
	limiteCentoventiGiorni := now.AddDate(0, 0, CleanFrom)

	// Mappiamo i job trovati nella finestra raccogliendo le informazioni necessarie
	type jobInfo struct {
		hasStatus bool
		keys      []string
	}
	jobs := make(map[string]*jobInfo)

	// 1. Elenchiamo solo i giorni compresi nella finestra [-120, -30],
	// un prefisso "canzoniere/YYYY/MM/DD/" alla volta.
	for day := limiteCentoventiGiorni; !day.After(limiteTrentaGiorni); day = day.AddDate(0, 0, 1) {
		prefix := fmt.Sprintf("canzoniere/%s/", day.Format("2006/01/02"))
		keys, err := s3ListObjects(cfg, prefix)
		if err != nil {
			log.Printf("[Pulizia S3] Errore nell'elencare %s: %v", prefix, err)
			continue // un giorno non raggiungibile non deve bloccare gli altri
		}

		for _, key := range keys {
			// Le chiavi sono nel formato: canzoniere/<YYYY>/<MM>/<DD>/<jobID>/<filename>
			parts := strings.Split(key, "/")
			if len(parts) < 3 {
				continue
			}
			jobID := parts[len(parts)-2]
			filename := parts[len(parts)-1]

			if _, exists := jobs[jobID]; !exists {
				jobs[jobID] = &jobInfo{}
			}
			if filename == "status.json" {
				jobs[jobID].hasStatus = true
			}
			jobs[jobID].keys = append(jobs[jobID].keys, key)
		}
	}

	contatoreEliminati := 0

	// 2. Ogni job trovato è per costruzione già nella finestra corretta
	// (è comparso durante il listing di un giorno valido): non serve
	// scaricare né riparsare lo status.json per ricontrollare la data.
	for jobID, info := range jobs {
		if !info.hasStatus {
			continue // Se manca status.json non abbiamo un punto di riferimento certo: saltiamo per prudenza
		}

		log.Printf("[Pulizia S3] Cartella del job %s idonea per la pulizia", jobID)

		// Elimina tutti i file associati al job ECCETTO status.json
		for _, fileKey := range info.keys {
			if strings.HasSuffix(fileKey, "status.json") {
				continue // Preserviamo la richiesta / metadati
			}
			if err := s3DeleteObject(cfg, fileKey); err != nil {
				log.Printf("[Pulizia S3] Errore rimozione file %s: %v", fileKey, err)
			} else {
				contatoreEliminati++
			}
		}
	}

	log.Printf("[Pulizia S3] Fine pulizia. Rimossi complessivamente %d file obsoleti.", contatoreEliminati)
}

// ════════════════════════════════════════════════════════════════════════════
// Songs API — lettura DB
// ════════════════════════════════════════════════════════════════════════════

func scanSong(cfg Config, row *sql.Row) (*Song, error) {
	var s Song
	var autore, album, tonalita, famiglia, gruppo, momenti,
		dataRev, trasc, testo, testoS sql.NullString
	err := row.Scan(
		&s.Identificatore, &s.Titolo,
		&autore, &album, &tonalita, &famiglia, &gruppo, &momenti,
		&dataRev, &trasc, &testo, &testoS,
	)
	if err != nil {
		return nil, err
	}
	s.Autore, s.Album, s.Tonalita = nullStr(autore), nullStr(album), nullStr(tonalita)
	s.Famiglia, s.Gruppo, s.Momenti = nullStr(famiglia), nullStr(gruppo), nullStr(momenti)
	s.DataRevisione, s.Trascrittore = nullStr(dataRev), nullStr(trasc)
	s.TestoCompleto, s.TestoSemplice = nullStr(testo), nullStr(testoS)
	s.PdfAccordiURL, s.PdfTestoURL = songPDFURLs(cfg, s.Identificatore)
	return &s, nil
}

func scanSummaries(cfg Config, rows *sql.Rows) ([]SongSummary, error) {
	var out []SongSummary
	for rows.Next() {
		var s SongSummary
		var autore, album, tonalita, famiglia, momenti sql.NullString
		if err := rows.Scan(&s.Identificatore, &s.Titolo,
			&autore, &album, &tonalita, &famiglia, &momenti); err != nil {
			return nil, err
		}
		s.Autore, s.Album, s.Tonalita = nullStr(autore), nullStr(album), nullStr(tonalita)
		s.Famiglia, s.Momenti = nullStr(famiglia), nullStr(momenti)
		s.PdfAccordiURL, s.PdfTestoURL = songPDFURLs(cfg, s.Identificatore)
		out = append(out, s)
	}
	if out == nil {
		out = []SongSummary{}
	}
	return out, rows.Err()
}

// GET /songs/:id
func handleGetSong(cfg Config, db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/songs/")
		if id == "" {
			writeError(w, http.StatusBadRequest, "identificatore mancante")
			return
		}
		row := db.QueryRow(`SELECT identificatore, titolo, autore, album, tonalita,
			famiglia, gruppo, momenti, data_revisione, trascrittore,
			testo_completo, testo_semplice FROM songs WHERE identificatore = ?`, id)
		song, err := scanSong(cfg, row)
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "canzone non trovata")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, song)
	}
}

// GET /autori
func handleGetAutori(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.Query(`SELECT DISTINCT autore FROM songs
			WHERE autore IS NOT NULL AND autore != ''
			ORDER BY autore COLLATE NOCASE`)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer rows.Close()
		var list []string
		for rows.Next() {
			var a string
			if err := rows.Scan(&a); err == nil {
				list = append(list, a)
			}
		}
		if list == nil {
			list = []string{}
		}
		writeJSON(w, http.StatusOK, list)
	}
}

// GET /famiglie
func handleGetFamiglie(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.Query(`SELECT DISTINCT LOWER(famiglia) FROM songs
			WHERE famiglia IS NOT NULL AND famiglia != ''
			ORDER BY 1`)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer rows.Close()
		seen := map[string]bool{}
		var list []string
		for rows.Next() {
			var f string
			if err := rows.Scan(&f); err == nil && !seen[f] {
				seen[f] = true
				list = append(list, f)
			}
		}
		if list == nil {
			list = []string{}
		}
		writeJSON(w, http.StatusOK, list)
	}
}

// GET /momenti
func handleGetMomenti(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.Query(`SELECT DISTINCT momenti FROM songs
			WHERE momenti IS NOT NULL AND momenti != ''`)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer rows.Close()
		seen := map[string]bool{}
		var list []string
		for rows.Next() {
			var m string
			if err := rows.Scan(&m); err != nil {
				continue
			}
			for _, part := range strings.Split(m, ";") {
				part = strings.TrimSpace(part)
				if part != "" && !seen[part] {
					seen[part] = true
					list = append(list, part)
				}
			}
		}
		if list == nil {
			list = []string{}
		}
		writeJSON(w, http.StatusOK, list)
	}
}

// GET /songs/autore/:autore
func handleSongsByAutore(cfg Config, db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		autore := strings.TrimPrefix(r.URL.Path, "/songs/autore/")
		if autore == "" {
			writeError(w, http.StatusBadRequest, "autore mancante")
			return
		}
		rows, err := db.Query(`SELECT identificatore, titolo, autore, album, tonalita,
			famiglia, momenti FROM songs WHERE autore LIKE ?
			ORDER BY titolo COLLATE NOCASE`, "%"+autore+"%")
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer rows.Close()
		list, err := scanSummaries(cfg, rows)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"count": len(list), "results": list})
	}
}

// GET /songs/famiglia/:famiglia
func handleSongsByFamiglia(cfg Config, db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		famiglia := strings.TrimPrefix(r.URL.Path, "/songs/famiglia/")
		if famiglia == "" {
			writeError(w, http.StatusBadRequest, "famiglia mancante")
			return
		}
		rows, err := db.Query(`SELECT identificatore, titolo, autore, album, tonalita,
			famiglia, momenti FROM songs WHERE LOWER(famiglia) = LOWER(?)
			ORDER BY titolo COLLATE NOCASE`, famiglia)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer rows.Close()
		list, err := scanSummaries(cfg, rows)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"count": len(list), "results": list})
	}
}

// GET /songs/momento/:momento
func handleSongsByMomento(cfg Config, db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		momento := strings.TrimPrefix(r.URL.Path, "/songs/momento/")
		if momento == "" {
			writeError(w, http.StatusBadRequest, "momento mancante")
			return
		}
		rows, err := db.Query(`SELECT identificatore, titolo, autore, album, tonalita,
			famiglia, momenti FROM songs
			WHERE momenti = ? OR momenti LIKE ? OR momenti LIKE ? OR momenti LIKE ?
			ORDER BY titolo COLLATE NOCASE`,
			momento, momento+";%", "%;"+momento+";%", "%;"+momento)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer rows.Close()
		list, err := scanSummaries(cfg, rows)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"count": len(list), "results": list})
	}
}

// GET /search?q=...&titolo=...&autore=...&famiglia=...&momento=...&tonalita=...&testo=...
func handleSearch(cfg Config, db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var conditions []string
		var args []any

		if v := strings.TrimSpace(q.Get("q")); v != "" {
			like := "%" + v + "%"
			conditions = append(conditions,
				`(titolo LIKE ? OR autore LIKE ? OR album LIKE ? OR testo_semplice LIKE ?)`)
			args = append(args, like, like, like, like)
		}
		for param, col := range map[string]string{
			"titolo": "titolo", "autore": "autore", "album": "album",
			"tonalita": "tonalita", "gruppo": "gruppo",
		} {
			if v := strings.TrimSpace(q.Get(param)); v != "" {
				conditions = append(conditions, col+` LIKE ?`)
				args = append(args, "%"+v+"%")
			}
		}
		if v := strings.TrimSpace(q.Get("famiglia")); v != "" {
			conditions = append(conditions, `LOWER(famiglia) LIKE LOWER(?)`)
			args = append(args, "%"+v+"%")
		}
		if v := strings.TrimSpace(q.Get("momento")); v != "" {
			conditions = append(conditions,
				`(momenti = ? OR momenti LIKE ? OR momenti LIKE ? OR momenti LIKE ?)`)
			args = append(args, v, v+";%", "%;"+v+";%", "%;"+v)
		}
		if v := strings.TrimSpace(q.Get("testo")); v != "" {
			like := "%" + v + "%"
			conditions = append(conditions, `(testo_semplice LIKE ? OR testo_completo LIKE ?)`)
			args = append(args, like, like)
		}

		where := ""
		if len(conditions) > 0 {
			where = "WHERE " + strings.Join(conditions, " AND ")
		}
		query := `SELECT identificatore, titolo, autore, album, tonalita, famiglia, momenti
			FROM songs ` + where + ` ORDER BY titolo COLLATE NOCASE`

		rows, err := db.Query(query, args...)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer rows.Close()
		list, err := scanSummaries(cfg, rows)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"count": len(list), "results": list})
	}
}

// ════════════════════════════════════════════════════════════════════════════
// Canzoniere generation
// ════════════════════════════════════════════════════════════════════════════

func buildDocumentClass(req *CanzonieRequest) string {
	formato := req.Formato
	if formato == "" {
		formato = "standard"
	}

	accordi := req.Accordi

	if accordi == "" {
		accordi = "lyric"
	}
	font := req.Font
	if font == "" {
		font = "libertine"
	}
	copertina := req.Copertina
	if copertina == "" {
		copertina = "cover"
	}
	opts := []string{formato, accordi, font}
	if req.MinorSign != "" {
		opts = append(opts, "minorsign="+req.MinorSign)
	}
	opts = append(opts, req.Indici...)
	opts = append(opts, copertina)
	return strings.Join(opts, ",")
}

func buildDocumentClassLyric(req *CanzonieRequest) string {
	c := *req
	c.Accordi = "lyric"
	return buildDocumentClass(&c)
}

func buildDocumentClassChorded(req *CanzonieRequest) string {
	c := *req
	c.Accordi = "chorded"
	return buildDocumentClass(&c)
}

// extractSongBody estrae il blocco \beginsong...\endsong di una canzone dal
// suo testo completo. Se semitoni != 0 inserisce \transpose{N} subito dopo la
// riga \beginsong; se includeMomenti è true inserisce anche \momenti{momenti}
// subito dopo (dopo l'eventuale \transpose) — usato quando è stato richiesto
// l'indice tematico, per popolare il relativo indice in fase di compilazione.
func extractSongBody(testoCompleto string, semitoni int, momenti string, includeMomenti bool) string {
	startIdx := strings.Index(testoCompleto, `\beginsong`)
	if startIdx == -1 {
		return ""
	}
	endMarker := `\endsong`
	endIdx := strings.Index(testoCompleto, endMarker)
	if endIdx == -1 {
		return ""
	}
	body := testoCompleto[startIdx : endIdx+len(endMarker)]
	if semitoni != 0 || includeMomenti {
		re := regexp.MustCompile(`(?m)(\\beginsong\{[^}]*\}(?:\[[^\]]*\])?)`)
		replaced := false
		body = re.ReplaceAllStringFunc(body, func(match string) string {
			if replaced {
				return match
			}
			replaced = true
			suffix := match
			if semitoni != 0 {
				suffix += fmt.Sprintf("\n\\transpose{%d}", semitoni)
			}
			if includeMomenti {
				suffix += fmt.Sprintf("\n\\momenti{%s}", momenti)
			}
			return suffix
		})
	}
	return body + "\n"
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// contentTypeForFile deduce il Content-Type dall'estensione, per l'upload S3
// dei file "extra" della working directory (stili, log, ausiliari LaTeX, ecc.)
// che non rientrano nei tipi già gestiti esplicitamente altrove.
func contentTypeForFile(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".pdf":
		return "application/pdf"
	case ".zip":
		return "application/zip"
	case ".json":
		return "application/json"
	case ".tex", ".sty", ".cls", ".log", ".aux", ".toc", ".idx", ".ind", ".ilg",
		".lof", ".lot", ".out", ".bbl", ".blg", ".xdy":
		return "text/plain; charset=utf-8"
	}
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

func zipFiles(files map[string]string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	for path, name := range files {
		f, err := zw.Create(name)
		if err != nil {
			zw.Close()
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			zw.Close()
			return nil, err
		}
		if _, err := f.Write(data); err != nil {
			zw.Close()
			return nil, err
		}
	}

	if err := zw.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func runCmd(workDir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func runXindy(workDir string, indici []string) string {
	type idxSpec struct{ idx, style string }
	specs := map[string]idxSpec{
		"titleindex":   {"alfabetico", "xindystyle"},
		"authorsindex": {"autori", "xindystyle"},
		"tematicindex": {"tematico", "xindytematic"},
	}
	var sb strings.Builder
	for _, indice := range indici {
		spec, ok := specs[indice]
		if !ok {
			continue
		}
		idxFile := spec.idx + ".idx"
		if _, err := os.Stat(filepath.Join(workDir, idxFile)); os.IsNotExist(err) {
			sb.WriteString(fmt.Sprintf("SKIP xindy %s (file non trovato)\n", idxFile))
			continue
		}
		out, err := runCmd(workDir, "xindy",
			"-M", "texindy", "-C", "utf8", "-L", "italian",
			"-M", spec.style, idxFile)
		sb.WriteString(fmt.Sprintf("--- xindy %s ---\n%s\n", idxFile, out))
		if err != nil {
			sb.WriteString(fmt.Sprintf("WARN xindy %s: %v\n", idxFile, err))
		}
	}
	return sb.String()
}

// runLatex: engine(1) → engine(2) → [xindy → engine(3) solo se ci sono indici]
// Il motore è configurabile via Config.LatexEngine ("lualatex" o "pdflatex").
func runLatex(cfg Config, workDir, texFile string, indici []string) (string, error) {
	var sb strings.Builder
	hasIndici := len(indici) > 0

	engine := cfg.LatexEngine
	if engine == "" {
		engine = "lualatex"
	}

	// pdflatex richiede --8bit per gestire correttamente i caratteri UTF-8 nel log
	baseArgs := []string{"--interaction=nonstopmode"}
	if engine == "pdflatex" {
		baseArgs = append(baseArgs, "--8bit")
	}

	runPass := func(label string) error {
		args := append(baseArgs, texFile)
		out, err := runCmd(workDir, engine, args...)
		sb.WriteString(fmt.Sprintf("--- %s [%s] ---\n%s\n", label, engine, out))
		return err
	}

	// Passata 1: costruisce riferimenti interni
	runPass("1a passata") //nolint — errore non fatale alla prima passata

	// Passata 2: risolve cross-reference; è l'ultima se non ci sono indici
	if err := runPass("2a passata"); err != nil && !hasIndici {
		return sb.String(), err
	}

	// Passata 3: solo se ci sono indici da incorporare
	if hasIndici {
		sb.WriteString("--- xindy ---\n")
		sb.WriteString(runXindy(workDir, indici))
		if err := runPass("3a passata"); err != nil {
			return sb.String(), err
		}
	}

	return sb.String(), nil
}

type GenerationResult struct {
	WorkDir  string
	ZipData  []byte
	ZipName  string
	LogChord string
	LogLyric string
	S3Files  []S3File // popolato se S3 è abilitato
}

// latexSem è un semaforo che limita a una sola compilazione LaTeX contemporanea.
// LuaLaTeX/pdflatex sono molto esosi di RAM; eseguirli in parallelo su un server
// piccolo causa OOM. Il canale bufferizzato da 1 garantisce la serializzazione.
// Usa un canale (invece di sync.Mutex) per consentire agevolmente un timeout futuro
// tramite select + time.After senza modificare la struttura.
var latexSem = make(chan struct{}, 1)

// generateCanzoniere compila il canzoniere e, se S3 è abilitato, carica tutto
// lo Space aggiornando status.json a ogni step. jobID può essere vuoto se S3
// non è abilitato (in quel caso lo stato non viene scritto da nessuna parte).
func generateCanzoniere(cfg Config, db *sql.DB, req *CanzonieRequest, jobID string) (*GenerationResult, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	status := &JobStatus{
		JobID:     jobID,
		Status:    "queued",
		Message:   "In coda",
		CreatedAt: now,
		UpdatedAt: now,
	}
	workDir, err := os.MkdirTemp("", "canzoniere-*")
	if err != nil {
		return nil, fmt.Errorf("mktemp: %w", err)
	}
	cleanup := func() { os.RemoveAll(workDir) }

	// Acquisisce il semaforo: se un'altra compilazione è in corso, attende.
	// Il defer garantisce il rilascio anche in caso di panic o return anticipato.
	latexSem <- struct{}{}
	defer func() { <-latexSem }()

	if err := copyDir(cfg.TemplatePath, workDir); err != nil {
		cleanup()
		return nil, fmt.Errorf("copia template: %w", err)
	}

	// ── Step 1: recupero canzoni ─────────────────────────────────────────────
	status.Status = "fetching_songs"
	status.Message = fmt.Sprintf("Recupero %d canzoni dal database", len(req.Canzoni))
	s3UpdateStatus(cfg.S3, status)

	// L'indice tematico (opzione documentclass "tematicindex") richiede che
	// ogni canzone porti con sé la sua specifica \momenti{...} subito dopo
	// \beginsong, usata dal pacchetto canzoniere per popolare l'indice.
	wantTematico := false
	for _, idx := range req.Indici {
		if idx == "tematicindex" {
			wantTematico = true
			break
		}
	}

	var songBlocks strings.Builder
	for _, entry := range req.Canzoni {
		row := db.QueryRow(`SELECT testo_completo, momenti FROM songs WHERE identificatore = ?`,
			entry.Identificatore)
		var tc, momenti sql.NullString
		if err := row.Scan(&tc, &momenti); err != nil {
			cleanup()
			if err == sql.ErrNoRows {
				return nil, fmt.Errorf("canzone non trovata: %s", entry.Identificatore)
			}
			return nil, fmt.Errorf("db: %w", err)
		}
		block := extractSongBody(tc.String, entry.Semitoni, nullStr(momenti), wantTematico)
		if block == "" {
			cleanup()
			return nil, fmt.Errorf("testo non estraibile: %s", entry.Identificatore)
		}
		songBlocks.WriteString(block)
		songBlocks.WriteString("\n")
	}

	texTemplate, err := os.ReadFile(filepath.Join(workDir, "canzoniere.tex"))
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("lettura canzoniere.tex: %w", err)
	}

	numCol := req.NumColonne
	if numCol <= 0 {
		numCol = 1
	}

	populate := func(opts string) string {
		s := string(texTemplate)
		s = strings.ReplaceAll(s, "##documentclass##", opts)
		s = strings.ReplaceAll(s, "##titolettocop##", req.Titoletto)
		s = strings.ReplaceAll(s, "##titolocop##", req.Titolo)
		s = strings.ReplaceAll(s, "##sottotitolocop##", req.Sottotitolo)
		s = strings.ReplaceAll(s, "##canzsongcolumsnumber##", fmt.Sprintf("%d", numCol))
		s = strings.ReplaceAll(s, "##CANZONI##", songBlocks.String())
		return s
	}

	wantLyric := (req.Accordi == "lyric" || req.Accordi == "both")
	wantChorded := (req.Accordi == "chorded" || req.Accordi == "both")

	chordTex := filepath.Join(workDir, "canzoniereonline.tex")
	if wantChorded {
		if err := os.WriteFile(chordTex, []byte(populate(buildDocumentClassChorded(req))), 0644); err != nil {
			cleanup()
			return nil, fmt.Errorf("scrittura canzoniereonline.tex: %w", err)
		}
	}

	lyricTex := filepath.Join(workDir, "canzoniereonline_lyrics.tex")
	if wantLyric {
		if err := os.WriteFile(lyricTex, []byte(populate(buildDocumentClassLyric(req))), 0644); err != nil {
			cleanup()
			return nil, fmt.Errorf("scrittura canzoniereonline_lyrics.tex: %w", err)
		}
	}

	// ── Step 2: compilazione LaTeX ──────────────────────────────────────────
	status.Status = "compiling"
	status.Message = fmt.Sprintf("Compilazione con %s in corso", cfg.LatexEngine)
	s3UpdateStatus(cfg.S3, status)
	var logChord string
	if wantChorded {
		logChord, err1 := runLatex(cfg, workDir, "canzoniereonline.tex", req.Indici)
		log.Print(logChord)
		if err1 != nil {
			log.Printf("WARN %s chord: %v", cfg.LatexEngine, err1)
		}
	}

	var logLyric string
	if wantLyric {
		var err2 error
		logLyric, err2 = runLatex(cfg, workDir, "canzoniereonline_lyrics.tex", nil)
		if err2 != nil {
			log.Printf("WARN %s lyric: %v", cfg.LatexEngine, err2)
		}
	}

	chordPDF := filepath.Join(workDir, "canzoniereonline.pdf")
	lyricPDF := filepath.Join(workDir, "canzoniereonline_lyrics.pdf")
	if wantChorded {
		if _, err := os.Stat(chordPDF); os.IsNotExist(err) {
			cleanup()
			return nil, fmt.Errorf("PDF con accordi non generato\nLog:\n%s", logChord)
		}
	}

	if wantLyric {
		if _, err := os.Stat(lyricPDF); os.IsNotExist(err) {
			cleanup()
			return nil, fmt.Errorf("PDF testi non generato\nLog:\n%s", logLyric)
		}
	}

	// ── Step 3: packaging ───────────────────────────────────────────────────
	status.Status = "packaging"
	status.Message = "Creazione archivio ZIP"
	s3UpdateStatus(cfg.S3, status)

	zipName := fmt.Sprintf("canzoniere_%s.zip", time.Now().Format("20060102_150405"))

	// Include TUTTI i file presenti nella working directory al termine della
	// compilazione: PDF, sorgenti .tex, file di stile/classe copiati dal
	// template, log, ausiliari (.aux/.toc/.idx/.ind/.ilg/...), output di
	// xindy, ecc. — non solo un sottoinsieme scelto a mano.
	filesToZip := map[string]string{}
	if err := filepath.Walk(workDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(workDir, path)
		if relErr != nil {
			return relErr
		}
		filesToZip[path] = rel
		return nil
	}); err != nil {
		cleanup()
		return nil, fmt.Errorf("scansione workdir per zip: %w", err)
	}

	zipData, err := zipFiles(filesToZip)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("zip: %w", err)
	}

	result := &GenerationResult{
		WorkDir:  workDir,
		ZipData:  zipData,
		ZipName:  zipName,
		LogChord: logChord,
		LogLyric: logLyric,
	}

	// ── Step 4: upload S3 ────────────────────────────────────────────────────
	if cfg.S3.Enabled && jobID != "" {
		status.Status = "uploading"
		status.Message = "Caricamento file sullo Space"
		s3UpdateStatus(cfg.S3, status)

		// Mappa nome-file → contenuto + content-type
		type upload struct {
			filename    string
			data        []byte
			contentType string
		}

		uploads := []upload{
			{zipName, zipData, "application/zip"},
		}

		// Carica TUTTI i file della working directory (gli stessi che finiscono
		// nello ZIP: PDF, tex, stili, log, ausiliari, ecc.), preservando eventuali
		// sottocartelle come "prefisso/" nella chiave S3.
		for path, rel := range filesToZip {
			data, err := os.ReadFile(path)
			if err != nil {
				log.Printf("WARN lettura %s per upload S3: %v", rel, err)
				continue
			}
			uploads = append(uploads, upload{rel, data, contentTypeForFile(rel)})
		}

		// Payload originale (per eventuale recupero dall'interfaccia)
		if payloadData, err := json.Marshal(req); err == nil {
			uploads = append(uploads, upload{"payload.json", payloadData, "application/json"})
		}

		var s3files []S3File
		for _, u := range uploads {
			key := s3Key(jobID, u.filename)
			if err := s3PutObject(cfg.S3, key, u.data, u.contentType); err != nil {
				log.Printf("WARN s3 upload %s: %v", u.filename, err)
				continue
			}
			s3files = append(s3files, S3File{
				Name:      u.filename,
				Key:       key,
				URL:       s3ObjectURL(cfg.S3, key),       // URL interno; richiede firma per accesso
				PUBLICURL: s3ObjectPublicURL(cfg.S3, key), // URL pubblico (se abilitato)
			})
		}
		result.S3Files = s3files

		// Status finale
		status.Status = "done"
		status.Message = "Canzoniere pronto"
		status.Files = s3files
		s3UpdateStatus(cfg.S3, status)
	}

	//svuota il canzoniere cache S3 in background, senza bloccare la risposta HTTP
	go SvuotaCartelleVecchie(cfg.S3, cfg.CleanFrom, cfg.CleanTo)

	return result, nil
}

// ════════════════════════════════════════════════════════════════════════════
// Email
// ════════════════════════════════════════════════════════════════════════════

func sendEmail(cfg Config, toAddr, subject, body, attachName string, attachData []byte) error {
	if cfg.SMTPUser == "" || cfg.SMTPPass == "" {
		return fmt.Errorf("SMTP non configurato")
	}
	auth := smtp.PlainAuth("", cfg.SMTPUser, cfg.SMTPPass, cfg.SMTPHost)
	boundary := "==CANZONIERE_BOUNDARY=="
	var msg bytes.Buffer
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString(fmt.Sprintf("From: %s\r\n", cfg.SMTPFrom))
	msg.WriteString(fmt.Sprintf("To: %s\r\n", toAddr))
	msg.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	msg.WriteString(fmt.Sprintf("Content-Type: multipart/mixed; boundary=\"%s\"\r\n\r\n", boundary))
	msg.WriteString(fmt.Sprintf("--%s\r\n", boundary))
	msg.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	msg.WriteString(body + "\r\n\r\n")
	if attachData != nil {
		msg.WriteString(fmt.Sprintf("--%s\r\n", boundary))
		msg.WriteString(fmt.Sprintf("Content-Type: application/zip; name=\"%s\"\r\n", attachName))
		msg.WriteString("Content-Transfer-Encoding: base64\r\n")
		msg.WriteString(fmt.Sprintf("Content-Disposition: attachment; filename=\"%s\"\r\n\r\n", attachName))
		encoded := base64.StdEncoding.EncodeToString(attachData)
		for i := 0; i < len(encoded); i += 76 {
			end := i + 76
			if end > len(encoded) {
				end = len(encoded)
			}
			msg.WriteString(encoded[i:end] + "\r\n")
		}
	}
	msg.WriteString(fmt.Sprintf("--%s--\r\n", boundary))
	return smtp.SendMail(cfg.SMTPHost+":"+cfg.SMTPPort, auth,
		cfg.SMTPFrom, []string{toAddr}, msg.Bytes())
}

// ════════════════════════════════════════════════════════════════════════════
// PDF "canzone singola" — cache basata su S3 + hash del testo
// ════════════════════════════════════════════════════════════════════════════
//
// Per ogni canzone del DB manteniamo su S3, sotto canzoni/<identificatore>/,
// due PDF generati con il template in modalità "canzone singola" (nessuna
// copertina, nessun indice):
//
//	canzoni/<identificatore>/<identificatore>.pdf        (con accordi)
//	canzoni/<identificatore>/<identificatore>_lyric.pdf  (solo testo)
//	canzoni/<identificatore>/sha256.txt                  (hash di testo_completo)
//
// sha256.txt permette di saltare la rigenerazione quando la canzone non è
// cambiata. La sincronizzazione (syncAllSongPDFs) va sempre lanciata in
// background, ed è agganciata a ogni holder.swap in startDBUpdater/main:
// parte cioè ogni volta che il DB viene (ri)caricato.

func songPDFPrefix(identificatore string) string {
	return fmt.Sprintf("canzoni/%s/", identificatore)
}

func songChordedPDFKey(identificatore string) string {
	return songPDFPrefix(identificatore) + identificatore + ".pdf"
}

func songLyricPDFKey(identificatore string) string {
	return songPDFPrefix(identificatore) + identificatore + "_lyric.pdf"
}

func songHashKey(identificatore string) string {
	return songPDFPrefix(identificatore) + "sha256.txt"
}

// songPDFURLs costruisce, senza alcuna chiamata di rete, gli URL pubblici dei
// due PDF "canzone singola" di un identificatore. Sono deterministici: non
// verificano che il file esista già su S3 in questo momento — la generazione
// effettiva è gestita in background da syncAllSongPDFs. Se S3 non è abilitato
// restituisce stringhe vuote.
func songPDFURLs(cfg Config, identificatore string) (accordiURL, testoURL string) {
	if !cfg.S3.Enabled || identificatore == "" {
		return "", ""
	}
	return s3ObjectPublicURL(cfg.S3, songChordedPDFKey(identificatore)),
		s3ObjectPublicURL(cfg.S3, songLyricPDFKey(identificatore))
}

// buildDocumentClassSingle costruisce le opzioni documentclass per il PDF di
// una singola canzone: modalità "singoli" (nessuna copertina, nessun indice),
// font Libertine, minorsign=m.
func buildDocumentClassSingle(accordi string) string {
	return strings.Join([]string{"singoli", accordi, "libertine", "minorsign=m"}, ",")
}

// ensureSongPDF verifica se i due PDF "canzone singola" di una canzone sono
// aggiornati rispetto al testo attuale (confronto sha256 con
// canzoni/<identificatore>/sha256.txt su S3). Se lo sono, non fa nulla e
// restituisce changed=false. Altrimenti compila entrambe le versioni, le
// carica su S3 e aggiorna l'hash per ultimo — così un fallimento a metà
// strada non lascia lo stato "falsamente aggiornato" per il tentativo dopo.
func ensureSongPDF(cfg Config, identificatore, testoCompleto string) (changed bool, err error) {
	if testoCompleto == "" {
		return false, fmt.Errorf("testo_completo vuoto")
	}
	hash := sha256Hex([]byte(testoCompleto))
	hashKey := songHashKey(identificatore)

	var existingHash string
	var isNotFound bool

	for attempt := 1; attempt <= 3; attempt++ {
		body, getErr := s3GetObject(cfg.S3, hashKey)

		if getErr == nil {
			// Lettura completata con successo
			existingBytes, _ := io.ReadAll(body)
			body.Close()
			existingHash = strings.TrimSpace(string(existingBytes))
			break
		}

		// Se l'errore è "chiave non trovata", il file non c'è (è una nuova canzone).
		// È inutile riprovare: usciamo subito dal ciclo.
		if strings.Contains(getErr.Error(), "chiave non trovata") {
			isNotFound = true
			break
		}

		// Se siamo qui, c'è stato un errore (timeout o rate limit 429).
		if attempt < 3 {
			log.Printf("[song-pdf-sync] WARN %s: check hash fallito (%v). Riprovo tra 1 secondo...", identificatore, getErr)
			time.Sleep(1 * time.Second)
		} else {
			// Dopo 3 tentativi gettiamo la spugna. Ritorniamo un errore così il
			// ciclo principale logga il fallimento e NON lancia LaTeX.
			return false, fmt.Errorf("verifica hash su S3 fallita dopo 3 tentativi: %w", getErr)
		}
	}

	// Confrontiamo l'hash (se il file esisteva)
	if !isNotFound && existingHash == hash {
		return false, nil // già aggiornato: nessuna rigenerazione necessaria
	}

	block := extractSongBody(testoCompleto, 0, "", false)
	if block == "" {
		return false, fmt.Errorf("testo non estraibile")
	}

	// Compilazione LaTeX vera e propria: serializzata sullo stesso semaforo
	// usato per generateCanzoniere, per evitare OOM su compilazioni concorrenti.
	latexSem <- struct{}{}
	defer func() { <-latexSem }()

	workDir, mkErr := os.MkdirTemp("", "song-*")
	if mkErr != nil {
		return false, fmt.Errorf("mktemp: %w", mkErr)
	}
	defer os.RemoveAll(workDir)

	if cpErr := copyDir(cfg.TemplatePath, workDir); cpErr != nil {
		return false, fmt.Errorf("copia template: %w", cpErr)
	}

	texTemplate, rdErr := os.ReadFile(filepath.Join(workDir, "canzoniere.tex"))
	if rdErr != nil {
		return false, fmt.Errorf("lettura canzoniere.tex: %w", rdErr)
	}

	populate := func(opts string) string {
		s := string(texTemplate)
		s = strings.ReplaceAll(s, "##documentclass##", opts)
		s = strings.ReplaceAll(s, "##titolettocop##", "")
		s = strings.ReplaceAll(s, "##titolocop##", "")
		s = strings.ReplaceAll(s, "##sottotitolocop##", "")
		s = strings.ReplaceAll(s, "##canzsongcolumsnumber##", "1")
		s = strings.ReplaceAll(s, "##CANZONI##", block)
		return s
	}

	if wErr := os.WriteFile(filepath.Join(workDir, "chorded.tex"),
		[]byte(populate(buildDocumentClassSingle("chorded"))), 0644); wErr != nil {
		return false, fmt.Errorf("scrittura chorded.tex: %w", wErr)
	}
	if wErr := os.WriteFile(filepath.Join(workDir, "lyric.tex"),
		[]byte(populate(buildDocumentClassSingle("lyric"))), 0644); wErr != nil {
		return false, fmt.Errorf("scrittura lyric.tex: %w", wErr)
	}

	if out, cErr := runLatex(cfg, workDir, "chorded.tex", nil); cErr != nil {
		return false, fmt.Errorf("compilazione accordi: %w\n%s", cErr, out)
	}
	if out, cErr := runLatex(cfg, workDir, "lyric.tex", nil); cErr != nil {
		return false, fmt.Errorf("compilazione testo: %w\n%s", cErr, out)
	}

	chordPDF, rErr := os.ReadFile(filepath.Join(workDir, "chorded.pdf"))
	if rErr != nil {
		return false, fmt.Errorf("lettura chorded.pdf: %w", rErr)
	}
	lyricPDF, rErr := os.ReadFile(filepath.Join(workDir, "lyric.pdf"))
	if rErr != nil {
		return false, fmt.Errorf("lettura lyric.pdf: %w", rErr)
	}

	if pErr := s3PutObject(cfg.S3, songChordedPDFKey(identificatore), chordPDF, "application/pdf"); pErr != nil {
		return false, fmt.Errorf("upload pdf accordi: %w", pErr)
	}
	if pErr := s3PutObject(cfg.S3, songLyricPDFKey(identificatore), lyricPDF, "application/pdf"); pErr != nil {
		return false, fmt.Errorf("upload pdf testo: %w", pErr)
	}
	if pErr := s3PutObject(cfg.S3, hashKey, []byte(hash), "text/plain; charset=utf-8"); pErr != nil {
		return false, fmt.Errorf("upload sha256: %w", pErr)
	}
	return true, nil
}

// syncAllSongPDFs itera tutte le canzoni del DB e, per ciascuna, verifica/
// rigenera i due PDF "canzone singola" tramite ensureSongPDF. Va sempre
// lanciata in una goroutine dedicata (mai in modo sincrono): con molte
// canzoni può richiedere minuti, dato che le compilazioni sono serializzate
// sullo stesso semaforo di generateCanzoniere. No-op se S3 non è abilitato.
func syncAllSongPDFs(cfg Config, db *sql.DB) {
	if !cfg.S3.Enabled {
		return
	}
	rows, err := db.Query(`SELECT identificatore, testo_completo FROM songs`)
	if err != nil {
		log.Printf("[song-pdf-sync] query fallita: %v", err)
		return
	}
	type pending struct{ id, testo string }
	var songs []pending
	for rows.Next() {
		var id string
		var testo sql.NullString
		if scanErr := rows.Scan(&id, &testo); scanErr != nil {
			log.Printf("[song-pdf-sync] scan fallito: %v", scanErr)
			continue
		}
		songs = append(songs, pending{id, testo.String})
	}
	if errIter := rows.Err(); errIter != nil {
		log.Printf("[song-pdf-sync] iterazione righe fallita: %v", errIter)
	}
	rows.Close()

	log.Printf("[song-pdf-sync] avvio sincronizzazione PDF per %d canzoni…", len(songs))
	updated, skipped, failed := 0, 0, 0
	start := time.Now()
	for _, s := range songs {
		changed, err := ensureSongPDF(cfg, s.id, s.testo)
		switch {
		case err != nil:
			failed++
			log.Printf("[song-pdf-sync] %s: errore: %v", s.id, err)
		case changed:
			updated++
			log.Printf("[song-pdf-sync] %s: rigenerato", s.id)
		default:
			skipped++
		}
	}
	log.Printf("[song-pdf-sync] completata in %v: %d rigenerate, %d invariate, %d errori",
		time.Since(start).Round(time.Second), updated, skipped, failed)
}

// ════════════════════════════════════════════════════════════════════════════
// Canzoniere handlers
// ════════════════════════════════════════════════════════════════════════════

// newJobID genera un identificatore nel formato <timestamp><64-char-hex>.
// Il timestamp (YYYYMMDDHHmm) rende i job ordinabili cronologicamente;
// i 64 caratteri hex garantiscono unicità e non indovinabilità.
func newJobID() string {
	ts := time.Now().UTC().Format("200601021504") // 12 cifre: YYYYMMDDHHmm
	h := sha256.New()
	h.Write([]byte(fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())))
	return ts + hex.EncodeToString(h.Sum(nil)) // 12 + 64 = 76 caratteri
}

// POST /canzoniere  →  asincrono, invia ZIP via email + carica su S3 se abilitato
func handleGenera(cfg Config, db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "solo POST")
			return
		}
		var req CanzonieRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "JSON non valido: "+err.Error())
			return
		}
		if len(req.Canzoni) == 0 {
			writeError(w, http.StatusBadRequest, "nessuna canzone specificata")
			return
		}
		if req.Email == "" {
			writeError(w, http.StatusBadRequest, "email destinatario mancante")
			return
		}

		jobID := newJobID()
		resp := map[string]string{
			"status":  "accepted",
			"job_id":  jobID,
			"message": fmt.Sprintf("Generazione avviata — riceverai lo ZIP a %s", req.Email),
		}
		if cfg.S3.Enabled {
			resp["status_url"] = cfg.S3.PublicEndpoint + cfg.S3.Bucket + "/" + s3Key(jobID, "status.json")
		}
		writeJSON(w, http.StatusAccepted, resp)

		// Scrivi status iniziale su S3 prima di staccare la goroutine
		if cfg.S3.Enabled {
			s3UpdateStatus(cfg.S3, &JobStatus{
				JobID:     jobID,
				Status:    "queued",
				Message:   "In coda",
				CreatedAt: time.Now().UTC().Format(time.RFC3339),
				UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			})
		}

		go func() {
			result, err := generateCanzoniere(cfg, db, &req, jobID)
			if err != nil {
				log.Printf("ERROR genera [%s]: %v", jobID, err)
				if cfg.S3.Enabled {
					s3UpdateStatus(cfg.S3, &JobStatus{
						JobID:     jobID,
						Status:    "error",
						Message:   "Generazione fallita",
						Error:     err.Error(),
						CreatedAt: time.Now().UTC().Format(time.RFC3339),
						UpdatedAt: time.Now().UTC().Format(time.RFC3339),
					})
				}
				_ = sendEmail(cfg, req.Email, "Errore generazione canzoniere",
					fmt.Sprintf("Errore:\n\n%v", err), "", nil)
				return
			}
			defer os.RemoveAll(result.WorkDir)
			titolo := req.Titolo
			if titolo == "" {
				titolo = "Canzoniere"
			}
			body := fmt.Sprintf("Ciao!\n\nIl canzoniere \"%s\" è pronto.\n"+
				"In allegato trovi lo ZIP con i PDF e i sorgenti TeX.\n\n"+
				"Buona musica!", titolo)
			if err := sendEmail(cfg, req.Email,
				fmt.Sprintf("Il tuo canzoniere: %s", titolo),
				body, result.ZipName, result.ZipData); err != nil {
				log.Printf("ERROR email a %s: %v", req.Email, err)
			} else {
				log.Printf("INFO \"%s\" [%s] inviato a %s", titolo, jobID, req.Email)
			}
		}()
	}
}

// POST /canzoniere/download  →  asincrono con jobID; ZIP disponibile via S3 quando status=done
func handleGeneraSync(cfg Config, db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "solo POST")
			return
		}
		var req CanzonieRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "JSON non valido: "+err.Error())
			return
		}
		if len(req.Canzoni) == 0 {
			writeError(w, http.StatusBadRequest, "nessuna canzone specificata")
			return
		}

		jobID := newJobID()
		resp := map[string]string{
			"status":  "accepted",
			"job_id":  jobID,
			"message": "Generazione avviata",
		}
		if cfg.S3.Enabled {
			resp["status_url"] = cfg.S3.PublicEndpoint + cfg.S3.Bucket + "/" + s3Key(jobID, "status.json")
		}
		writeJSON(w, http.StatusAccepted, resp)

		if cfg.S3.Enabled {
			s3UpdateStatus(cfg.S3, &JobStatus{
				JobID:     jobID,
				Status:    "queued",
				Message:   "In coda",
				CreatedAt: time.Now().UTC().Format(time.RFC3339),
				UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			})
		}

		go func() {
			result, err := generateCanzoniere(cfg, db, &req, jobID)
			if err != nil {
				log.Printf("ERROR download [%s]: %v", jobID, err)
				if cfg.S3.Enabled {
					s3UpdateStatus(cfg.S3, &JobStatus{
						JobID:     jobID,
						Status:    "error",
						Message:   "Generazione fallita",
						Error:     err.Error(),
						CreatedAt: time.Now().UTC().Format(time.RFC3339),
						UpdatedAt: time.Now().UTC().Format(time.RFC3339),
					})
				}
				return
			}
			defer os.RemoveAll(result.WorkDir)
			log.Printf("INFO download [%s] completato (%d file su S3)", jobID, len(result.S3Files))
		}()
	}
}

// GET /canzoniere/status/:jobID  →  proxy di status.json dallo Space S3
// GET /canzoniere/status/:jobID  →  legge status.json dallo Space con GET firmato SigV4.
// Il bucket può essere privato: la firma viene calcolata server-side e non è mai
// esposta al client. Il client interroga solo questo endpoint, non S3 direttamente.
func handleStatus(cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.S3.Enabled {
			writeError(w, http.StatusNotFound, "S3 non abilitato: stato non disponibile")
			return
		}
		jobID := strings.TrimPrefix(r.URL.Path, "/canzoniere/status/")
		if jobID == "" {
			writeError(w, http.StatusBadRequest, "job_id mancante")
			return
		}
		key := s3Key(jobID, "status.json")
		body, err := s3GetObject(cfg.S3, key)
		if err != nil {
			// Distingue "non trovato" da errori di rete/autenticazione
			if strings.Contains(err.Error(), "chiave non trovata") {
				writeError(w, http.StatusNotFound, "job non trovato")
			} else {
				writeError(w, http.StatusBadGateway, "errore lettura stato: "+err.Error())
			}
			return
		}
		defer body.Close()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, body)
	}
}

// ════════════════════════════════════════════════════════════════════════════
// Router
// ════════════════════════════════════════════════════════════════════════════

// GET /s3/<jobID>/<filename>  →  redirect 302 verso presigned URL del file su S3.
// Funziona come "punto fermo" indipendente dal provider: se si migra da R2 a Spaces
// (o viceversa) basta riconfigurare le variabili S3_* — gli URL /s3/... restano invariati.
// Il presigned URL ha durata configurabile via S3_REDIRECT_TTL_SECONDS (default 3600s).
func handleS3Redirect(cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.S3.Enabled {
			writeError(w, http.StatusNotFound, "S3 non abilitato")
			return
		}
		// Path atteso: /s3/<jobID>/<filename>
		rest := strings.TrimPrefix(r.URL.Path, "/s3/")
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			writeError(w, http.StatusBadRequest, "percorso non valido: atteso /s3/<jobID>/<filename>")
			return
		}
		jobID, filename := parts[0], parts[1]
		key := s3Key(jobID, filename)
		presigned, err := s3PresignedGetURL(cfg.S3, key, cfg.S3.RedirectTTL)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "errore generazione URL firmato: "+err.Error())
			return
		}
		http.Redirect(w, r, presigned, http.StatusFound)
	}
}

// GET /s3/<jobID>/download  →  scarica tutti i file della cartella come ZIP al volo.
// Il server fa GET firmato di ogni file dal bucket e li aggrega in uno ZIP in-memory
// che viene inviato direttamente al client senza toccare il disco.
// Nota: /download è una parola riservata e non può essere un filename reale su S3.
func handleS3DownloadZip(cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.S3.Enabled {
			writeError(w, http.StatusNotFound, "S3 non abilitato")
			return
		}
		jobID := strings.TrimPrefix(r.URL.Path, "/s3/")
		jobID = strings.TrimSuffix(jobID, "/download")
		if jobID == "" {
			writeError(w, http.StatusBadRequest, "jobID mancante")
			return
		}

		prefix := s3JobPrefix(jobID)
		keys, err := s3ListObjects(cfg.S3, prefix)
		if err != nil {
			writeError(w, http.StatusBadGateway, "errore lista file: "+err.Error())
			return
		}
		if len(keys) == 0 {
			writeError(w, http.StatusNotFound, "nessun file trovato per questo job")
			return
		}

		// Scarica ogni file e lo aggiunge allo ZIP in-memory
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for _, key := range keys {
			body, err := s3GetObject(cfg.S3, key)
			if err != nil {
				log.Printf("WARN s3 zip: skip %s: %v", key, err)
				continue
			}
			// Nome del file nello ZIP = solo la parte dopo il prefix (es. "canzoniere.pdf")
			name := strings.TrimPrefix(key, prefix)
			f, err := zw.Create(name)
			if err != nil {
				body.Close()
				continue
			}
			_, _ = io.Copy(f, body)
			body.Close()
		}
		if err := zw.Close(); err != nil {
			writeError(w, http.StatusInternalServerError, "errore creazione ZIP: "+err.Error())
			return
		}

		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition",
			fmt.Sprintf(`attachment; filename="canzoniere_%s.zip"`, jobID[:12])) // solo la parte timestamp
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(buf.Bytes())
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func newRouter(cfg Config, holder *dbHolder) http.Handler {
	mux := http.NewServeMux()

	// db() restituisce sempre il *sql.DB corrente, anche dopo uno swap a caldo.
	db := func() *sql.DB { return holder.get() }

	// ── Songs API ──────────────────────────────────────────────────────────
	mux.HandleFunc("/autori", func(w http.ResponseWriter, r *http.Request) { handleGetAutori(db())(w, r) })
	mux.HandleFunc("/famiglie", func(w http.ResponseWriter, r *http.Request) { handleGetFamiglie(db())(w, r) })
	mux.HandleFunc("/momenti", func(w http.ResponseWriter, r *http.Request) { handleGetMomenti(db())(w, r) })
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) { handleSearch(cfg, db())(w, r) })
	mux.HandleFunc("/songs/autore/", func(w http.ResponseWriter, r *http.Request) { handleSongsByAutore(cfg, db())(w, r) })
	mux.HandleFunc("/songs/famiglia/", func(w http.ResponseWriter, r *http.Request) { handleSongsByFamiglia(cfg, db())(w, r) })
	mux.HandleFunc("/songs/momento/", func(w http.ResponseWriter, r *http.Request) { handleSongsByMomento(cfg, db())(w, r) })
	mux.HandleFunc("/songs/", func(w http.ResponseWriter, r *http.Request) { handleGetSong(cfg, db())(w, r) })

	// ── Canzoniere ─────────────────────────────────────────────────────────
	mux.HandleFunc("/canzoniere/status/", handleStatus(cfg))
	mux.HandleFunc("/canzoniere/download", func(w http.ResponseWriter, r *http.Request) { handleGeneraSync(cfg, db())(w, r) })
	mux.HandleFunc("/canzoniere/debug", func(w http.ResponseWriter, r *http.Request) { handleDebugTex(cfg, db())(w, r) })
	mux.HandleFunc("/canzoniere", func(w http.ResponseWriter, r *http.Request) { handleGenera(cfg, db())(w, r) })

	// ── S3 proxy ───────────────────────────────────────────────────────────
	// /s3/<jobID>/download  →  ZIP di tutti i file del job (deve stare prima di /s3/)
	// /s3/<jobID>/<filename> →  redirect presigned al singolo file
	mux.HandleFunc("/s3/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/download") {
			handleS3DownloadZip(cfg)(w, r)
		} else {
			handleS3Redirect(cfg)(w, r)
		}
	})

	// ── Health ─────────────────────────────────────────────────────────────
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			writeError(w, http.StatusNotFound, "endpoint non trovato")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"service": "canzoniere-api", "status": "ok",
		})
	})

	return corsMiddleware(mux)
}

// ════════════════════════════════════════════════════════════════════════════
// DB auto-update da GitHub Releases
// ════════════════════════════════════════════════════════════════════════════

// dbHolder wrappa *sql.DB con un RWMutex per consentire lo swap atomico a caldo.
// Le goroutine che stanno servendo richieste tengono il read-lock; lo swap
// acquisisce il write-lock solo per il tempo necessario a sostituire il puntatore,
// poi chiude il vecchio DB fuori dal lock (le connessioni aperte vengono rilasciate
// naturalmente dal GC una volta che le query in corso terminano).
type dbHolder struct {
	mu sync.RWMutex
	db *sql.DB
}

func (h *dbHolder) get() *sql.DB {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.db
}

func (h *dbHolder) swap(newDB *sql.DB) {
	h.mu.Lock()
	old := h.db
	h.db = newDB
	h.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
}

// githubLatestAssetURL risolve l'URL di download dell'asset .db nell'ultima release.
//
// Accetta due forme di DB_RELEASE_URL:
//   - URL diretto all'asset:   https://github.com/<o>/<r>/releases/download/<tag>/songs.db
//     → viene usato così com'è; il tag viene estratto dal path per il log.
//   - URL del repository:      https://github.com/<owner>/<repo>
//     → interroga l'API GitHub /repos/<o>/<r>/releases/latest e cerca il primo asset .db.
func githubLatestAssetURL(releaseURL string) (downloadURL string, tag string, err error) {
	if strings.Contains(releaseURL, "/releases/download/") {
		parts := strings.SplitN(releaseURL, "/releases/download/", 2)
		tag = strings.SplitN(parts[1], "/", 2)[0]
		return releaseURL, tag, nil
	}

	// Converte https://github.com/<o>/<r> → https://api.github.com/repos/<o>/<r>/releases/latest
	apiURL := strings.Replace(releaseURL, "https://github.com/", "https://api.github.com/repos/", 1)
	apiURL = strings.TrimSuffix(apiURL, "/") + "/releases/latest"

	resp, err := http.Get(apiURL) //nolint:noctx
	if err != nil {
		return "", "", fmt.Errorf("github API: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("github API: HTTP %d", resp.StatusCode)
	}

	var rel struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", "", fmt.Errorf("github API decode: %w", err)
	}
	for _, a := range rel.Assets {
		if strings.HasSuffix(a.Name, ".db") {
			return a.BrowserDownloadURL, rel.TagName, nil
		}
	}
	return "", "", fmt.Errorf("nessun asset .db trovato nella release %s", rel.TagName)
}

// downloadDB scarica il DB dall'URL indicato e lo salva in destPath tramite
// un file temporaneo + os.Rename atomico, così destPath non è mai corrotto
// anche in caso di download interrotto.
func downloadDB(dlURL, destPath string) error {
	resp, err := http.Get(dlURL) //nolint:noctx
	if err != nil {
		return fmt.Errorf("download DB: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download DB: HTTP %d", resp.StatusCode)
	}

	tmp := destPath + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, destPath) // atomico su Linux
}

// openReadOnlyDB apre il DB SQLite in sola lettura con WAL e verifica che sia raggiungibile.
func openReadOnlyDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", path+"?mode=ro&_journal_mode=WAL")
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// startDBUpdater gestisce il ciclo di vita del DB:
//  1. Se il file non esiste ancora lo scarica subito (bloccante — il server non parte
//     finché non ha un DB valido).
//  2. Apre il DB e lo inserisce nel holder.
//  3. Avvia una goroutine in background che, dopo una pausa iniziale di 30 minuti
//     (per non stressare GitHub al deploy), controlla periodicamente se è uscita
//     una nuova release e, se sì, scarica il nuovo DB e fa lo swap a caldo.
//
// Se DB_RELEASE_URL non è configurata la funzione è no-op.
func startDBUpdater(cfg Config, holder *dbHolder) error {
	if cfg.DBReleaseURL == "" {
		return nil
	}

	interval := time.Duration(cfg.DBUpdateIntervalSec) * time.Second

	tryUpdate := func(currentTag string) (newTag string) {
		dlURL, tag, err := githubLatestAssetURL(cfg.DBReleaseURL)
		if err != nil {
			log.Printf("[db-updater] errore risoluzione URL: %v", err)
			return currentTag
		}
		if tag == currentTag {
			log.Printf("[db-updater] DB già aggiornato (%s), prossimo check tra %v", tag, interval)
			return currentTag
		}
		log.Printf("[db-updater] nuova release %s → scarico DB…", tag)
		if err := downloadDB(dlURL, cfg.DBPath); err != nil {
			log.Printf("[db-updater] download fallito: %v", err)
			return currentTag
		}
		newDB, err := openReadOnlyDB(cfg.DBPath)
		if err != nil {
			log.Printf("[db-updater] apertura nuovo DB fallita: %v", err)
			return currentTag
		}
		holder.swap(newDB)
		log.Printf("[db-updater] DB aggiornato a %s ✓", tag)
		go syncAllSongPDFs(cfg, newDB)
		return tag
	}

	// Download iniziale sincrono se il file non esiste
	if _, err := os.Stat(cfg.DBPath); os.IsNotExist(err) {
		log.Printf("[db-updater] DB non trovato, scarico da GitHub Releases…")
		dlURL, tag, err := githubLatestAssetURL(cfg.DBReleaseURL)
		if err != nil {
			return fmt.Errorf("impossibile risolvere URL release: %w", err)
		}
		if err := downloadDB(dlURL, cfg.DBPath); err != nil {
			return fmt.Errorf("download iniziale fallito: %w", err)
		}
		log.Printf("[db-updater] DB scaricato (%s) ✓", tag)
	}

	// Apri il DB (già presente o appena scaricato)
	db, err := openReadOnlyDB(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("apertura DB: %w", err)
	}
	holder.swap(db)
	go syncAllSongPDFs(cfg, db)

	// Loop di aggiornamento in background
	go func() {
		// Pausa iniziale: evita di chiamare l'API GitHub subito al deploy
		time.Sleep(30 * time.Minute)
		currentTag := ""
		for {
			currentTag = tryUpdate(currentTag)
			time.Sleep(interval)
		}
	}()

	return nil
}

// ════════════════════════════════════════════════════════════════════════════
// Main
// ════════════════════════════════════════════════════════════════════════════

func main() {
	cfg := loadConfig()

	if _, err := os.Stat(cfg.TemplatePath); os.IsNotExist(err) {
		log.Fatalf("template non trovata: %s", cfg.TemplatePath)
	}

	holder := &dbHolder{}

	if cfg.DBReleaseURL != "" {
		// Modalità auto-update: scarica il DB se mancante, poi si aggiorna in background.
		if err := startDBUpdater(cfg, holder); err != nil {
			log.Fatalf("db-updater: %v", err)
		}
	} else {
		// Modalità classica: DB_PATH deve esistere sul disco.
		db, err := sql.Open("sqlite3", cfg.DBPath+"?mode=ro&_journal_mode=WAL")
		if err != nil {
			log.Fatalf("apertura DB: %v", err)
		}
		defer db.Close()
		if err := db.Ping(); err != nil {
			log.Fatalf("connessione DB: %v", err)
		}
		holder.swap(db)
		go syncAllSongPDFs(cfg, db)
	}

	log.Printf("canzoniere-api :%s | DB: %s | template: %s | update-interval: %ds",
		cfg.Port, cfg.DBPath, cfg.TemplatePath, cfg.DBUpdateIntervalSec)
	if err := http.ListenAndServe(":"+cfg.Port, newRouter(cfg, holder)); err != nil {
		log.Fatalf("server: %v", err)
	}
}

// POST /canzoniere/debug  — come /download ma restituisce JSON con log completi
// invece del PDF; non elimina la workdir (utile per ispezionare i file generati)
func handleDebugTex(cfg Config, db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "solo POST")
			return
		}
		var req CanzonieRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "JSON non valido: "+err.Error())
			return
		}
		if len(req.Canzoni) == 0 {
			writeError(w, http.StatusBadRequest, "nessuna canzone specificata")
			return
		}

		result, genErr := generateCanzoniere(cfg, db, &req, "") // jobID vuoto: debug non usa S3

		resp := map[string]any{}
		if genErr != nil {
			resp["error"] = genErr.Error()
		} else {
			resp["ok"] = true
			resp["zip_size_bytes"] = len(result.ZipData)
		}

		if result != nil {
			resp["log_chorded"] = result.LogChord
			resp["log_lyric"] = result.LogLyric

			// Lista file nella workdir
			var files []string
			filepath.Walk(result.WorkDir, func(path string, info os.FileInfo, _ error) error {
				if !info.IsDir() {
					rel, _ := filepath.Rel(result.WorkDir, path)
					files = append(files, fmt.Sprintf("%s (%d B)", rel, info.Size()))
				}
				return nil
			})
			resp["workdir_files"] = files

			// Contenuto dei file .idx (per diagnosticare indici vuoti)
			idxContents := map[string]string{}
			for _, name := range []string{"alfabetico.idx", "autori.idx", "tematico.idx"} {
				if data, err := os.ReadFile(filepath.Join(result.WorkDir, name)); err == nil {
					idxContents[name] = string(data)
				}
			}
			if len(idxContents) > 0 {
				resp["idx_contents"] = idxContents
			}

			// Non elimina la workdir — logga il path per ispezione manuale nel container
			log.Printf("DEBUG workdir mantenuta: %s", result.WorkDir)
		}

		writeJSON(w, http.StatusOK, resp)
	}
}
