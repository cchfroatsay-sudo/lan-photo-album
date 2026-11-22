package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

const version = "1.0.2"

type App struct {
	root         string
	password     string
	secret       []byte
	thumbDir     string
	maxUpload    int64
	cookieMaxAge time.Duration
	thumbQ       chan string
	thumbMu      sync.Mutex
	thumbPending map[string]struct{}

	index *Indexer
}

func main() {
	listen := flag.String("listen", "127.0.0.1:8088", "listen address")
	root := flag.String("root", "./photos", "photo root directory")
	thumbCache := flag.String("thumb-cache", "./thumbs", "thumbnail cache directory")
	indexFile := flag.String("index", "", "content index file (default: <root parent>/.photosrv-index.json)")
	maxUploadGB := flag.Int64("max-upload-gb", 20, "maximum size per uploaded file in GiB")
	cookieHours := flag.Int("cookie-hours", 24*30, "login lifetime in hours")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	password := os.Getenv("PHOTOSRV_PASSWORD")
	if password == "" {
		log.Fatal("PHOTOSRV_PASSWORD is required")
	}
	debug.SetMemoryLimit(64 << 20)

	secret := []byte(os.Getenv("PHOTOSRV_SECRET"))
	if len(secret) < 16 {
		var err error
		secret, err = randomBytes(32)
		if err != nil {
			log.Fatal(err)
		}
		log.Print("warning: PHOTOSRV_SECRET is missing or too short; using a temporary secret")
	}

	if err := os.MkdirAll(*thumbCache, 0777); err != nil {
		log.Printf("warning: cannot create thumbnail cache: %v", err)
	}

	app := &App{
		root:         filepath.Clean(*root),
		password:     password,
		secret:       secret,
		thumbDir:     filepath.Clean(*thumbCache),
		maxUpload:    *maxUploadGB * 1024 * 1024 * 1024,
		cookieMaxAge: time.Duration(*cookieHours) * time.Hour,
		thumbQ:       make(chan string, 64),
		thumbPending: make(map[string]struct{}),
	}
	if *indexFile == "" {
		*indexFile = filepath.Join(filepath.Dir(app.root), ".photosrv-index.json")
	}
	app.index = NewIndexer(app.root, *indexFile)
	if app.index.Load() {
		log.Printf("index: loaded %d files from %s", app.index.Count(), *indexFile)
	} else {
		log.Printf("index: %s missing or invalid; building in background", *indexFile)
	}
	app.index.Start()
	go app.thumbWorker()

	mux := http.NewServeMux()
	mux.HandleFunc("/", app.handleRoot)
	mux.HandleFunc("/style.css", app.handleCSS)
	mux.HandleFunc("/health", app.handleHealth)
	mux.HandleFunc("/login", app.handleLogin)
	mux.HandleFunc("/logout", app.handleLogout)
	mux.HandleFunc("/gallery", app.authPage(app.handleGallery))
	mux.HandleFunc("/upload", app.handleUploadRoute)
	mux.HandleFunc("/view", app.authPage(app.handleView))
	mux.HandleFunc("/media", app.authAPI(app.handleMedia))
	mux.HandleFunc("/thumb", app.authAPI(app.handleThumb))
	mux.HandleFunc("/mkdir", app.authPage(app.handleMkdir))
	mux.HandleFunc("/delete", app.authPage(app.handleDelete))

	server := &http.Server{
		Addr:              *listen,
		Handler:           app.withHeaders(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	log.Printf("photosrv %s listening on %s, root=%s", version, *listen, app.root)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func (a *App) withHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

func (a *App) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if a.isAuthed(r) {
		http.Redirect(w, r, "/gallery", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (a *App) isAuthed(r *http.Request) bool {
	cookie, err := r.Cookie("photosrv_auth")
	if err != nil {
		return false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 3 || parts[0] != "v1" {
		return false
	}
	var expiry int64
	if _, err := fmt.Sscanf(parts[1], "%d", &expiry); err != nil {
		return false
	}
	if time.Now().Unix() > expiry {
		return false
	}
	payload := "v1." + parts[1]
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(payload))
	expected := hex.EncodeToString(mac.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(expected), []byte(parts[2])) == 1
}

func (a *App) setAuthCookie(w http.ResponseWriter) {
	expiry := time.Now().Add(a.cookieMaxAge).Unix()
	payload := fmt.Sprintf("v1.%d", expiry)
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(payload))
	token := payload + "." + hex.EncodeToString(mac.Sum(nil))
	http.SetCookie(w, &http.Cookie{
		Name:     "photosrv_auth",
		Value:    token,
		Path:     "/",
		MaxAge:   int(a.cookieMaxAge.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (a *App) clearAuthCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     "photosrv_auth",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (a *App) authPage(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.isAuthed(r) {
			target := r.URL.RequestURI()
			http.Redirect(w, r, "/login?next="+url.QueryEscape(target), http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

func (a *App) authAPI(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.isAuthed(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func safeNext(target string) string {
	if target == "" || !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") {
		return "/gallery"
	}
	if strings.HasPrefix(target, "/login") {
		return "/gallery"
	}
	return target
}

func cleanRel(input string) (string, error) {
	input = strings.ReplaceAll(input, "\\", "/")
	input = strings.TrimSpace(input)
	input = strings.TrimPrefix(input, "/")
	if input == "" {
		return "", nil
	}
	if strings.ContainsRune(input, 0) {
		return "", errors.New("invalid path")
	}

	parts := make([]string, 0, 4)
	for _, part := range strings.Split(input, "/") {
		part = strings.TrimSpace(part)
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return "", errors.New("invalid path")
		}
		if strings.ContainsAny(part, "\r\n\x00") {
			return "", errors.New("invalid path")
		}
		if len([]rune(part)) > 160 {
			part = string([]rune(part)[:160])
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "/"), nil
}

func (a *App) fullPath(rel string) (string, error) {
	rel, err := cleanRel(rel)
	if err != nil {
		return "", err
	}
	full := filepath.Join(a.root, filepath.FromSlash(rel))
	check, err := filepath.Rel(a.root, full)
	if err != nil || check == ".." || strings.HasPrefix(check, ".."+string(os.PathSeparator)) {
		return "", errors.New("invalid path")
	}
	if info, err := os.Lstat(full); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("symlinks are not allowed")
	}
	return full, nil
}

func (a *App) folderPath(folder string) (string, string, error) {
	rel, err := cleanRel(folder)
	if err != nil {
		return "", "", err
	}
	if rel == "" {
		rel = "iPhone备份/未命名手机"
	}
	if len(strings.Split(rel, "/")) > 5 {
		return "", "", errors.New("folder is too deep")
	}
	full, err := a.fullPath(rel)
	if err != nil {
		return "", "", err
	}
	return full, rel, nil
}

func safeFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	name = strings.Trim(name, ".")
	if name == "" {
		name = "upload-" + time.Now().Format("20060102-150405")
	}
	runes := []rune(name)
	if len(runes) > 180 {
		ext := filepath.Ext(name)
		base := strings.TrimSuffix(name, ext)
		baseRunes := []rune(base)
		if len(baseRunes) > 160 {
			baseRunes = baseRunes[:160]
		}
		name = string(baseRunes) + ext
	}
	return name
}

func uniqueName(dir, name string) string {
	target := filepath.Join(dir, name)
	if _, err := os.Lstat(target); os.IsNotExist(err) {
		return target
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 1; i < 10000; i++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
		if _, err := os.Lstat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%d%s", base, time.Now().UnixNano(), ext))
}

func randomBytes(n int) ([]byte, error) {
	buf := make([]byte, n)
	_, err := rand.Read(buf)
	return buf, err
}

func randomHex(n int) string {
	buf, err := randomBytes(n)
	if err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func formatBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value := float64(n)
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	for _, suffix := range units {
		value /= unit
		if value < unit {
			if value >= 10 {
				return fmt.Sprintf("%.1f %s", value, suffix)
			}
			return fmt.Sprintf("%.2f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f EB", value/unit)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04")
}

func isImage(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".heic", ".heif", ".bmp", ".tif", ".tiff":
		return true
	}
	return false
}

func isVideo(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp4", ".mov", ".m4v", ".avi", ".mkv", ".3gp", ".webm":
		return true
	}
	return false
}

func mediaType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".heic":
		return "image/heic"
	case ".heif":
		return "image/heif"
	case ".bmp":
		return "image/bmp"
	case ".tif", ".tiff":
		return "image/tiff"
	case ".mp4":
		return "video/mp4"
	case ".m4v":
		return "video/x-m4v"
	case ".mov":
		return "video/quicktime"
	case ".webm":
		return "video/webm"
	case ".3gp":
		return "video/3gpp"
	case ".avi":
		return "video/x-msvideo"
	case ".mkv":
		return "video/x-matroska"
	}
	return "application/octet-stream"
}

func diskFree(path string) (uint64, bool) {
	return platformDiskFree(path)
}
