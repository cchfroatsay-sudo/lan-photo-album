package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	stddraw "image/draw"

	xdraw "golang.org/x/image/draw"
)

type Entry struct {
	Name      string
	Rel       string
	Kind      string
	SizeText  string
	ModText   string
	ThumbURL  string
	MediaURL  string
	ViewURL   string
	Download  string
	Extension string
}

type Crumb struct {
	Name string
	URL  string
}

type GalleryData struct {
	Rel      string
	Title    string
	Free     string
	Crumbs   []Crumb
	Dirs     []Entry
	Files    []Entry
	Count    int
	Page     int
	Pages    int
	PrevURL  string
	NextURL  string
	Parent   string
	HasItems bool
}

type UploadData struct {
	Free          string
	DefaultFolder string
}

type DuplicateFile struct {
	Name     string `json:"name"`
	Existing string `json:"existing"`
	Size     string `json:"size"`
}

type ViewData struct {
	Rel         string
	Name        string
	Kind        string
	SizeText    string
	ModText     string
	MediaURL    string
	DownloadURL string
	DirRel      string
	DirURL      string
	PrevURL     string
	NextURL     string
	PrevName    string
	NextName    string
}

type LoginData struct {
	Next       string
	NextAction string
	Error      string
	Free       string
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.URL.Query().Get("next"))
	if r.Method == http.MethodGet {
		if a.isAuthed(r) {
			http.Redirect(w, r, next, http.StatusSeeOther)
			return
		}
		free, _ := diskFree(a.root)
		a.renderPage(w, r, http.StatusOK, "登录", false, "login", LoginData{
			Next:       next,
			NextAction: "/login?next=" + url.QueryEscape(next),
			Free:       freeText(free, a.root),
		})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	password := r.FormValue("password")
	if subtle.ConstantTimeCompare([]byte(password), []byte(a.password)) != 1 {
		time.Sleep(400 * time.Millisecond)
		free, _ := diskFree(a.root)
		a.renderPage(w, r, http.StatusUnauthorized, "登录", false, "login", LoginData{
			Next:       next,
			NextAction: "/login?next=" + url.QueryEscape(next),
			Error:      "密码不正确",
			Free:       freeText(free, a.root),
		})
		return
	}
	a.setAuthCookie(w)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	a.clearAuthCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, "ok")
}

func (a *App) handleCSS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	fmt.Fprint(w, styleCSS)
}

func (a *App) handleGallery(w http.ResponseWriter, r *http.Request) {
	rel, err := cleanRel(r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	full, err := a.fullPath(rel)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "cannot read storage", http.StatusInternalServerError)
		return
	}
	if !info.IsDir() {
		http.Redirect(w, r, "/view?path="+url.QueryEscape(rel), http.StatusSeeOther)
		return
	}

	dirEntries, err := os.ReadDir(full)
	if err != nil {
		http.Error(w, "cannot read folder", http.StatusInternalServerError)
		return
	}

	all := make([]Entry, 0, len(dirEntries))
	for _, dirEntry := range dirEntries {
		name := dirEntry.Name()
		if strings.HasPrefix(name, ".") || name == "$RECYCLE.BIN" || name == "System Volume Information" {
			continue
		}
		itemInfo, err := dirEntry.Info()
		if err != nil {
			continue
		}
		itemRel := path.Join(rel, name)
		entry := Entry{
			Name:      name,
			Rel:       itemRel,
			ModText:   formatTime(itemInfo.ModTime()),
			SizeText:  formatBytes(uint64(itemInfo.Size())),
			Extension: strings.ToLower(filepath.Ext(name)),
		}
		if dirEntry.IsDir() {
			entry.Kind = "dir"
		} else if isImage(name) {
			entry.Kind = "image"
			entry.ThumbURL = "/thumb?path=" + url.QueryEscape(itemRel)
			entry.MediaURL = "/media?path=" + url.QueryEscape(itemRel)
			entry.ViewURL = "/view?path=" + url.QueryEscape(itemRel)
			entry.Download = "/media?path=" + url.QueryEscape(itemRel) + "&download=1"
		} else if isVideo(name) {
			entry.Kind = "video"
			entry.MediaURL = "/media?path=" + url.QueryEscape(itemRel)
			entry.ViewURL = "/view?path=" + url.QueryEscape(itemRel)
			entry.Download = "/media?path=" + url.QueryEscape(itemRel) + "&download=1"
		} else {
			entry.Kind = "file"
			entry.MediaURL = "/media?path=" + url.QueryEscape(itemRel)
			entry.Download = "/media?path=" + url.QueryEscape(itemRel) + "&download=1"
		}
		all = append(all, entry)
	}

	sort.Slice(all, func(i, j int) bool {
		if all[i].Kind == "dir" && all[j].Kind != "dir" {
			return true
		}
		if all[i].Kind != "dir" && all[j].Kind == "dir" {
			return false
		}
		return strings.ToLower(all[i].Name) < strings.ToLower(all[j].Name)
	})

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	const perPage = 60
	pages := (len(all) + perPage - 1) / perPage
	if pages == 0 {
		pages = 1
	}
	if page > pages {
		page = pages
	}
	start := (page - 1) * perPage
	end := start + perPage
	if end > len(all) {
		end = len(all)
	}

	dirs := make([]Entry, 0)
	files := make([]Entry, 0)
	if start < len(all) {
		for _, entry := range all[start:end] {
			if entry.Kind == "dir" {
				dirs = append(dirs, entry)
			} else {
				files = append(files, entry)
			}
		}
	}

	crumbs := []Crumb{{Name: "照片首页", URL: "/gallery"}}
	if rel != "" {
		parts := strings.Split(rel, "/")
		for index, part := range parts {
			crumbs = append(crumbs, Crumb{
				Name: part,
				URL:  "/gallery?path=" + url.QueryEscape(strings.Join(parts[:index+1], "/")),
			})
		}
	}

	parent := ""
	if rel != "" {
		parentRel := path.Dir(rel)
		if parentRel == "." {
			parentRel = ""
		}
		parent = "/gallery?path=" + url.QueryEscape(parentRel)
	}

	free, _ := diskFree(a.root)
	data := GalleryData{
		Rel:      rel,
		Title:    path.Base(rel),
		Free:     freeText(free, a.root),
		Crumbs:   crumbs,
		Dirs:     dirs,
		Files:    files,
		Count:    len(all),
		Page:     page,
		Pages:    pages,
		Parent:   parent,
		HasItems: len(all) > 0,
	}
	if rel == "" {
		data.Title = "照片首页"
	}
	if page > 1 {
		data.PrevURL = galleryURL(rel, page-1)
	}
	if page < pages {
		data.NextURL = galleryURL(rel, page+1)
	}
	a.renderPage(w, r, http.StatusOK, data.Title, true, "gallery", data)
}

func (a *App) handleUploadRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		a.authAPI(a.handleUpload)(w, r)
		return
	}
	a.authPage(a.handleUploadPage)(w, r)
}

func (a *App) handleUploadPage(w http.ResponseWriter, r *http.Request) {
	free, _ := diskFree(a.root)
	a.renderPage(w, r, http.StatusOK, "上传照片", true, "upload", UploadData{
		Free:          freeText(free, a.root),
		DefaultFolder: "iPhone备份/iPhone",
	})
}

func (a *App) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	folder := r.URL.Query().Get("folder")
	if folder == "" {
		folder = "iPhone备份/未命名手机"
	}
	dest, folderRel, err := a.folderPath(folder)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid folder"})
		return
	}
	_, statErr := os.Stat(dest)
	destExisted := statErr == nil
	if err := os.MkdirAll(dest, 0777); err != nil {
		log.Printf("mkdir %s: %v", dest, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "cannot create folder"})
		return
	}

	reader, err := r.MultipartReader()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid multipart body"})
		return
	}

	saved := make([]string, 0)
	duplicates := make([]DuplicateFile, 0)
	failures := make([]string, 0)
	indexTouched := false
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Printf("multipart: %v", err)
			break
		}
		if part.FormName() != "files" || part.FileName() == "" {
			io.Copy(io.Discard, part)
			part.Close()
			continue
		}

		name := safeFilename(part.FileName())
		tempName := ".upload-" + randomHex(8) + ".tmp"
		tempPath := filepath.Join(dest, tempName)
		file, err := os.OpenFile(tempPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0666)
		if err != nil {
			failures = append(failures, name+": cannot create file")
			part.Close()
			continue
		}
		imageUpload := isImage(name)
		hasher := sha256.New()
		var writer io.Writer = file
		if imageUpload {
			writer = io.MultiWriter(file, hasher)
		}
		written, copyErr := io.Copy(writer, io.LimitReader(part, a.maxUpload+1))
		closeErr := file.Close()
		part.Close()

		if copyErr != nil || closeErr != nil {
			os.Remove(tempPath)
			failures = append(failures, name+": write failed")
			continue
		}
		if written == 0 {
			os.Remove(tempPath)
			failures = append(failures, name+": empty file")
			continue
		}
		if written > a.maxUpload {
			os.Remove(tempPath)
			failures = append(failures, name+": file is too large")
			continue
		}

		var finalPath string
		if imageUpload {
			sum := hex.EncodeToString(hasher.Sum(nil))
			indexTouched = true
			a.index.Lock()
			if existing, duplicate := a.index.FindDuplicateLocked(sum, written); duplicate {
				a.index.Unlock()
				os.Remove(tempPath)
				duplicates = append(duplicates, DuplicateFile{
					Name:     name,
					Existing: existing,
					Size:     formatBytes(uint64(written)),
				})
				log.Printf("duplicate from %s: %s (%s), existing %s", r.RemoteAddr, name, formatBytes(uint64(written)), existing)
				continue
			}
			finalPath = uniqueName(dest, name)
			if err := os.Rename(tempPath, finalPath); err != nil {
				a.index.Unlock()
				os.Remove(tempPath)
				failures = append(failures, name+": save failed")
				continue
			}
			modTime := time.Now().UnixNano()
			if info, err := os.Stat(finalPath); err == nil {
				modTime = info.ModTime().UnixNano()
			}
			if rel, err := filepath.Rel(a.root, finalPath); err == nil {
				a.index.AddLocked(rel, written, modTime, sum)
			}
			a.index.Unlock()
		} else {
			finalPath = uniqueName(dest, name)
			if err := os.Rename(tempPath, finalPath); err != nil {
				os.Remove(tempPath)
				failures = append(failures, name+": save failed")
				continue
			}
		}
		saved = append(saved, filepath.Base(finalPath))
		log.Printf("upload from %s: %s (%s) -> %s", r.RemoteAddr, name, formatBytes(uint64(written)), folderRel)
	}

	if indexTouched {
		a.index.ScheduleSave()
	}
	if len(saved) == 0 && !destExisted {
		os.Remove(dest)
	}
	duplicateGallery := ""
	if len(duplicates) > 0 {
		parent := path.Dir(duplicates[0].Existing)
		if parent == "." {
			parent = ""
		}
		duplicateGallery = "/gallery?path=" + url.QueryEscape(parent)
	}
	status := http.StatusOK
	if len(saved) == 0 && len(duplicates) == 0 {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, map[string]any{
		"ok":                len(saved) > 0 || len(duplicates) > 0,
		"folder":            folderRel,
		"saved":             saved,
		"duplicates":        duplicates,
		"errors":            failures,
		"gallery":           "/gallery?path=" + url.QueryEscape(folderRel),
		"duplicate_gallery": duplicateGallery,
	})
}

func (a *App) handleMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rel, err := cleanRel(r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	full, err := a.fullPath(rel)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	a.serveFile(w, r, full, r.URL.Query().Get("download") == "1")
}

func (a *App) serveFile(w http.ResponseWriter, r *http.Request, full string, download bool) {
	file, err := os.Open(full)
	if err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "cannot open file", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	name := filepath.Base(full)
	contentType := mediaType(name)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	disposition := "inline"
	if download {
		disposition = "attachment"
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename*=UTF-8''%s", disposition, url.PathEscape(name)))
	http.ServeContent(w, r, name, info.ModTime(), file)
}

func (a *App) handleThumb(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rel, err := cleanRel(r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	full, err := a.fullPath(rel)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	ext := strings.ToLower(filepath.Ext(full))
	if ext != ".jpg" && ext != ".jpeg" && ext != ".png" {
		a.serveFile(w, r, full, false)
		return
	}

	cachePath := filepath.Join(a.thumbDir, thumbCacheKey(rel, info.Size(), info.ModTime().UnixNano())+".jpg")
	if data, err := os.ReadFile(cachePath); err == nil {
		serveThumbBytes(w, r, data, info.ModTime(), 86400)
		return
	}

	// Never decode a full-resolution photo in the request path: on this router
	// that blocks for seconds per image and can exhaust memory. Phones embed a
	// small JPEG thumbnail in EXIF, which is enough for the grid and costs
	// microseconds to locate.
	if thumb, orientation, err := exifInfo(full); err == nil && len(thumb) > 0 {
		serveThumbBytes(w, r, orientJPEG(thumb, orientation), info.ModTime(), 120)
		a.enqueueThumb(rel)
		return
	}

	a.enqueueThumb(rel)
	a.serveFile(w, r, full, false)
}

func thumbCacheKey(rel string, size, modTime int64) string {
	key := sha256.Sum256([]byte(fmt.Sprintf("v2|%s|%d|%d", rel, size, modTime)))
	return hex.EncodeToString(key[:])
}

func serveThumbBytes(w http.ResponseWriter, r *http.Request, data []byte, modTime time.Time, maxAge int) {
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", fmt.Sprintf("private, max-age=%d", maxAge))
	http.ServeContent(w, r, "thumb.jpg", modTime, bytes.NewReader(data))
}

func (a *App) enqueueThumb(rel string) {
	if rel == "" {
		return
	}
	a.thumbMu.Lock()
	if _, busy := a.thumbPending[rel]; busy {
		a.thumbMu.Unlock()
		return
	}
	a.thumbPending[rel] = struct{}{}
	a.thumbMu.Unlock()
	select {
	case a.thumbQ <- rel:
	default:
		a.thumbMu.Lock()
		delete(a.thumbPending, rel)
		a.thumbMu.Unlock()
	}
}

// thumbWorker warms the sharp thumbnail cache one file at a time while the
// router is otherwise idle, so browsing stays responsive.
func (a *App) thumbWorker() {
	for rel := range a.thumbQ {
		a.warmThumb(rel)
		a.thumbMu.Lock()
		delete(a.thumbPending, rel)
		a.thumbMu.Unlock()
		time.Sleep(time.Second)
	}
}

func (a *App) warmThumb(rel string) {
	full, err := a.fullPath(rel)
	if err != nil {
		return
	}
	info, err := os.Stat(full)
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	ext := strings.ToLower(filepath.Ext(full))
	if ext != ".jpg" && ext != ".jpeg" && ext != ".png" {
		return
	}
	cachePath := filepath.Join(a.thumbDir, thumbCacheKey(rel, info.Size(), info.ModTime().UnixNano())+".jpg")
	if _, err := os.Stat(cachePath); err == nil {
		return
	}
	if available := platformMemAvailable(); available > 0 && available < 40*1024*1024 {
		log.Printf("thumbnail %s: skipped, only %d MB available", rel, available/1024/1024)
		return
	}
	started := time.Now()
	_, orientation, _ := exifInfo(full)
	data, err := makeThumb(full, orientation)
	if err != nil {
		log.Printf("thumbnail %s: %v", rel, err)
		return
	}
	debug.FreeOSMemory()
	if err := os.MkdirAll(a.thumbDir, 0777); err != nil {
		return
	}
	tempPath := cachePath + ".tmp-" + randomHex(4)
	if err := os.WriteFile(tempPath, data, 0666); err != nil {
		os.Remove(tempPath)
		return
	}
	if err := os.Rename(tempPath, cachePath); err != nil {
		os.Remove(tempPath)
		return
	}
	a.cleanupThumbs(120 * 1024 * 1024)
	log.Printf("thumbnail %s: %d ms", rel, time.Since(started).Milliseconds())
}

func makeThumb(full string, orientation int) ([]byte, error) {
	file, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var img image.Image
	switch strings.ToLower(filepath.Ext(full)) {
	case ".jpg", ".jpeg":
		img, err = jpeg.Decode(file)
	case ".png":
		img, err = png.Decode(file)
	default:
		return nil, fmt.Errorf("unsupported thumbnail format")
	}
	if err != nil {
		return nil, err
	}

	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width < 1 || height < 1 {
		return nil, fmt.Errorf("empty image")
	}
	const maxSide = 420
	scale := math.Min(float64(maxSide)/float64(width), float64(maxSide)/float64(height))
	if scale > 1 {
		scale = 1
	}
	targetWidth := int(float64(width) * scale)
	targetHeight := int(float64(height) * scale)
	if targetWidth < 1 {
		targetWidth = 1
	}
	if targetHeight < 1 {
		targetHeight = 1
	}

	target := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	stddraw.Draw(target, target.Bounds(), image.White, image.Point{}, stddraw.Src)
	xdraw.ApproxBiLinear.Scale(target, target.Bounds(), img, bounds, stddraw.Over, nil)

	oriented := applyOrientation(target, orientation)
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, oriented, &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func (a *App) cleanupThumbs(limit int64) {
	entries, err := os.ReadDir(a.thumbDir)
	if err != nil {
		return
	}
	type cacheFile struct {
		path    string
		size    int64
		modTime time.Time
	}
	files := make([]cacheFile, 0, len(entries))
	var total int64
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || info.IsDir() {
			continue
		}
		files = append(files, cacheFile{
			path:    filepath.Join(a.thumbDir, entry.Name()),
			size:    info.Size(),
			modTime: info.ModTime(),
		})
		total += info.Size()
	}
	if total <= limit {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.Before(files[j].modTime) })
	for _, file := range files {
		if total <= limit {
			break
		}
		if os.Remove(file.path) == nil {
			total -= file.size
		}
	}
}

func (a *App) handleView(w http.ResponseWriter, r *http.Request) {
	rel, err := cleanRel(r.URL.Query().Get("path"))
	if err != nil || rel == "" {
		http.Redirect(w, r, "/gallery", http.StatusSeeOther)
		return
	}
	full, err := a.fullPath(rel)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	info, err := os.Stat(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if info.IsDir() {
		http.Redirect(w, r, "/gallery?path="+url.QueryEscape(rel), http.StatusSeeOther)
		return
	}

	kind := "file"
	if isImage(rel) {
		kind = "image"
	} else if isVideo(rel) {
		kind = "video"
	}
	data := ViewData{
		Rel:         rel,
		Name:        filepath.Base(rel),
		Kind:        kind,
		SizeText:    formatBytes(uint64(info.Size())),
		ModText:     formatTime(info.ModTime()),
		MediaURL:    "/media?path=" + url.QueryEscape(rel),
		DownloadURL: "/media?path=" + url.QueryEscape(rel) + "&download=1",
		DirRel:      path.Dir(rel),
	}
	if data.DirRel == "." {
		data.DirRel = ""
	}
	data.DirURL = "/gallery?path=" + url.QueryEscape(data.DirRel)

	dirFull, err := a.fullPath(data.DirRel)
	if err == nil {
		if entries, err := os.ReadDir(dirFull); err == nil {
			names := make([]string, 0, len(entries))
			for _, entry := range entries {
				if entry.IsDir() || (!isImage(entry.Name()) && !isVideo(entry.Name())) {
					continue
				}
				names = append(names, entry.Name())
			}
			sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
			current := filepath.Base(rel)
			for index, name := range names {
				if name != current {
					continue
				}
				if index > 0 {
					prevRel := path.Join(data.DirRel, names[index-1])
					data.PrevURL = "/view?path=" + url.QueryEscape(prevRel)
					data.PrevName = names[index-1]
				}
				if index < len(names)-1 {
					nextRel := path.Join(data.DirRel, names[index+1])
					data.NextURL = "/view?path=" + url.QueryEscape(nextRel)
					data.NextName = names[index+1]
				}
				break
			}
		}
	}
	a.renderPage(w, r, http.StatusOK, data.Name, true, "view", data)
}

func (a *App) handleMkdir(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	parent, err := cleanRel(r.FormValue("path"))
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	name, err := cleanRel(r.FormValue("name"))
	if err != nil || name == "" || strings.Contains(name, "/") {
		http.Error(w, "invalid folder name", http.StatusBadRequest)
		return
	}
	newRel := path.Join(parent, name)
	full, err := a.fullPath(newRel)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	if err := os.Mkdir(full, 0777); err != nil {
		if os.IsExist(err) {
			http.Error(w, "folder already exists", http.StatusConflict)
			return
		}
		http.Error(w, "cannot create folder", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/gallery?path="+url.QueryEscape(newRel), http.StatusSeeOther)
}

func (a *App) handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	rel, err := cleanRel(r.FormValue("path"))
	if err != nil || rel == "" {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	full, err := a.fullPath(rel)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	info, err := os.Stat(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if info.IsDir() {
		http.Error(w, "cannot delete folder", http.StatusBadRequest)
		return
	}
	if err := os.Remove(full); err != nil {
		log.Printf("delete %s: %v", rel, err)
		http.Error(w, "cannot delete file", http.StatusInternalServerError)
		return
	}
	if a.index.Remove(rel) {
		if err := a.index.Save(); err != nil {
			log.Printf("index: save after delete %s: %v", rel, err)
		}
	}
	log.Printf("delete from %s: %s", r.RemoteAddr, rel)
	parent := path.Dir(rel)
	if parent == "." {
		parent = ""
	}
	http.Redirect(w, r, "/gallery?path="+url.QueryEscape(parent), http.StatusSeeOther)
}

func galleryURL(rel string, page int) string {
	values := url.Values{}
	if rel != "" {
		values.Set("path", rel)
	}
	if page > 1 {
		values.Set("page", strconv.Itoa(page))
	}
	if len(values) == 0 {
		return "/gallery"
	}
	return "/gallery?" + values.Encode()
}

func freeText(free uint64, root string) string {
	if free == 0 {
		if _, err := os.Stat(root); err != nil {
			return "存储未挂载"
		}
		return ""
	}
	return "可用 " + formatBytes(free)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}
