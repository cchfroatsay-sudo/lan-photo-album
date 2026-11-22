package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

const (
	indexVersion          = 1
	indexSaveDelay        = 2 * time.Second
	hashReadyDelay        = 3 * time.Second
	startupReconcileDelay = 10 * time.Minute
	reconcileInterval     = 6 * time.Hour
	reconcileFallback     = 30 * time.Minute
)

type indexRecord struct {
	Hash    string `json:"hash"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"mtime"`
}

type indexDisk struct {
	Version int           `json:"version"`
	Files   []indexRecord `json:"files"`
}

type Indexer struct {
	root      string
	indexPath string

	mu     sync.Mutex
	byHash map[string][]indexRecord
	byPath map[string]indexRecord
	loaded bool

	scanMu sync.Mutex
	saveMu sync.Mutex
	timer  *time.Timer

	pendingMu   sync.Mutex
	pending     map[string]time.Time
	pendingWake chan struct{}

	watcherOK atomic.Bool
}

func NewIndexer(root, indexPath string) *Indexer {
	return &Indexer{
		root:        root,
		indexPath:   indexPath,
		byHash:      make(map[string][]indexRecord),
		byPath:      make(map[string]indexRecord),
		pending:     make(map[string]time.Time),
		pendingWake: make(chan struct{}, 1),
	}
}

func (ix *Indexer) Load() bool {
	data, err := os.ReadFile(ix.indexPath)
	if err != nil {
		return false
	}
	var disk indexDisk
	if err := json.Unmarshal(data, &disk); err != nil || disk.Version != indexVersion {
		log.Printf("index: ignoring invalid index file: %v", err)
		return false
	}

	byHash := make(map[string][]indexRecord)
	byPath := make(map[string]indexRecord)
	for _, record := range disk.Files {
		if !validIndexRecord(record) {
			continue
		}
		if _, exists := byPath[record.Path]; exists {
			continue
		}
		byPath[record.Path] = record
		byHash[record.Hash] = append(byHash[record.Hash], record)
	}

	ix.mu.Lock()
	ix.byHash = byHash
	ix.byPath = byPath
	ix.loaded = true
	ix.mu.Unlock()
	return true
}

func (ix *Indexer) Count() int {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return len(ix.byPath)
}

func (ix *Indexer) Start() {
	if !ix.loaded {
		go ix.Build()
	}
	go ix.watchLoop()
	go ix.pendingLoop()
	go ix.reconcileLoop()
}

func (ix *Indexer) Lock() {
	ix.mu.Lock()
}

func (ix *Indexer) Unlock() {
	ix.mu.Unlock()
}

func (ix *Indexer) FindDuplicateLocked(hash string, size int64) (string, bool) {
	records := append([]indexRecord(nil), ix.byHash[hash]...)
	for _, record := range records {
		full := filepath.Join(ix.root, filepath.FromSlash(record.Path))
		info, err := os.Stat(full)
		if err != nil || !info.Mode().IsRegular() || info.Size() != size {
			ix.removeLocked(record.Path)
			continue
		}
		if info.ModTime().UnixNano() != record.ModTime {
			ix.removeLocked(record.Path)
			continue
		}
		return record.Path, true
	}
	return "", false
}

func (ix *Indexer) AddLocked(path string, size, modTime int64, hash string) {
	path = filepath.ToSlash(path)
	if old, ok := ix.byPath[path]; ok {
		ix.removeHashLocked(old)
	}
	record := indexRecord{
		Hash:    hash,
		Path:    path,
		Size:    size,
		ModTime: modTime,
	}
	ix.byPath[path] = record
	ix.byHash[hash] = append(ix.byHash[hash], record)
}

func (ix *Indexer) Remove(path string) bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.removeLocked(path)
}

func (ix *Indexer) removeLocked(path string) bool {
	path = filepath.ToSlash(path)
	record, ok := ix.byPath[path]
	if !ok {
		return false
	}
	ix.removeHashLocked(record)
	delete(ix.byPath, path)
	return true
}

func (ix *Indexer) removeHashLocked(record indexRecord) {
	records := ix.byHash[record.Hash]
	for index, candidate := range records {
		if candidate.Path != record.Path {
			continue
		}
		records = append(records[:index], records[index+1:]...)
		break
	}
	if len(records) == 0 {
		delete(ix.byHash, record.Hash)
		return
	}
	ix.byHash[record.Hash] = records
}

func (ix *Indexer) RemovePrefix(path string) bool {
	path = filepath.ToSlash(path)
	ix.mu.Lock()
	defer ix.mu.Unlock()
	removed := false
	if record, ok := ix.byPath[path]; ok {
		ix.removeHashLocked(record)
		delete(ix.byPath, path)
		removed = true
	}
	prefix := path + "/"
	for candidate, record := range ix.byPath {
		if !strings.HasPrefix(candidate, prefix) {
			continue
		}
		ix.removeHashLocked(record)
		delete(ix.byPath, candidate)
		removed = true
	}
	return removed
}

func (ix *Indexer) Save() error {
	ix.mu.Lock()
	records := make([]indexRecord, 0, len(ix.byPath))
	for _, record := range ix.byPath {
		records = append(records, record)
	}
	ix.mu.Unlock()
	sort.Slice(records, func(i, j int) bool { return records[i].Path < records[j].Path })

	data, err := json.Marshal(indexDisk{Version: indexVersion, Files: records})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(ix.indexPath), 0777); err != nil {
		return err
	}

	ix.saveMu.Lock()
	defer ix.saveMu.Unlock()
	tempPath := ix.indexPath + ".tmp"
	file, err := os.OpenFile(tempPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		os.Remove(tempPath)
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(tempPath)
		return err
	}
	if err := file.Close(); err != nil {
		os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, ix.indexPath); err != nil {
		os.Remove(tempPath)
		return err
	}
	return nil
}

func (ix *Indexer) ScheduleSave() {
	ix.saveMu.Lock()
	defer ix.saveMu.Unlock()
	if ix.timer != nil {
		ix.timer.Stop()
	}
	ix.timer = time.AfterFunc(indexSaveDelay, func() {
		if err := ix.Save(); err != nil {
			log.Printf("index: save failed: %v", err)
		}
	})
}

func (ix *Indexer) Build() {
	ix.scanMu.Lock()
	defer ix.scanMu.Unlock()
	ix.waitForRoot()
	started := time.Now()
	seen := make(map[string]indexRecord)
	count := 0
	_ = filepath.WalkDir(ix.root, func(full string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := entry.Name()
		if entry.IsDir() {
			if full != ix.root && skipIndexDir(name) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || !isImage(name) {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(ix.root, full)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		sum, err := hashFile(full)
		if err != nil {
			log.Printf("index: hash %s: %v", rel, err)
			return nil
		}
		seen[rel] = indexRecord{
			Hash:    sum,
			Path:    rel,
			Size:    info.Size(),
			ModTime: info.ModTime().UnixNano(),
		}
		count++
		if count%20 == 0 {
			time.Sleep(50 * time.Millisecond)
		}
		return nil
	})
	ix.pruneSeen(seen)

	ix.mu.Lock()
	for path, record := range ix.byPath {
		if _, ok := seen[path]; ok {
			continue
		}
		full := filepath.Join(ix.root, filepath.FromSlash(path))
		if info, err := os.Stat(full); err == nil && info.Mode().IsRegular() {
			seen[path] = record
		}
	}
	ix.byPath = seen
	ix.byHash = make(map[string][]indexRecord)
	for _, record := range seen {
		ix.byHash[record.Hash] = append(ix.byHash[record.Hash], record)
	}
	ix.loaded = true
	ix.mu.Unlock()

	if err := ix.Save(); err != nil {
		log.Printf("index: initial save failed: %v", err)
	}
	log.Printf("index: built %d files in %s", len(seen), time.Since(started).Round(time.Millisecond))
}

func (ix *Indexer) Reconcile() {
	ix.scanMu.Lock()
	defer ix.scanMu.Unlock()
	if _, err := os.Stat(ix.root); err != nil {
		return
	}
	started := time.Now()
	seen := make(map[string]indexRecord)
	changed := false
	hashed := 0
	scanned := 0
	_ = filepath.WalkDir(ix.root, func(full string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := entry.Name()
		if entry.IsDir() {
			if full != ix.root && skipIndexDir(name) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || !isImage(name) {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(ix.root, full)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		scanned++
		if scanned%200 == 0 {
			time.Sleep(20 * time.Millisecond)
		}
		ix.mu.Lock()
		record, known := ix.byPath[rel]
		ix.mu.Unlock()
		if known && record.Size == info.Size() && record.ModTime == info.ModTime().UnixNano() {
			seen[rel] = record
			return nil
		}
		if time.Since(info.ModTime()) < 5*time.Second {
			return nil
		}
		sum, err := hashFile(full)
		if err != nil {
			return nil
		}
		seen[rel] = indexRecord{
			Hash:    sum,
			Path:    rel,
			Size:    info.Size(),
			ModTime: info.ModTime().UnixNano(),
		}
		changed = true
		hashed++
		if hashed%20 == 0 {
			time.Sleep(50 * time.Millisecond)
		}
		return nil
	})
	ix.pruneSeen(seen)

	ix.mu.Lock()
	for path, record := range ix.byPath {
		if _, ok := seen[path]; ok {
			continue
		}
		full := filepath.Join(ix.root, filepath.FromSlash(path))
		if info, err := os.Stat(full); err == nil && info.Mode().IsRegular() {
			seen[path] = record
			continue
		}
		ix.removeHashLocked(record)
		delete(ix.byPath, path)
		changed = true
	}
	for path, record := range seen {
		if old, ok := ix.byPath[path]; ok && old == record {
			continue
		} else if ok {
			ix.removeHashLocked(old)
		}
		ix.byPath[path] = record
		ix.byHash[record.Hash] = append(ix.byHash[record.Hash], record)
		changed = true
	}
	ix.mu.Unlock()

	if changed {
		if err := ix.Save(); err != nil {
			log.Printf("index: reconcile save failed: %v", err)
		}
	}
	if changed || hashed > 0 {
		log.Printf("index: reconcile %d files (hashed %d) in %s", len(seen), hashed, time.Since(started).Round(time.Millisecond))
	}
}

func (ix *Indexer) reconcileLoop() {
	time.Sleep(startupReconcileDelay)
	for {
		ix.Reconcile()
		interval := reconcileInterval
		if !ix.watcherOK.Load() {
			interval = reconcileFallback
		}
		time.Sleep(interval)
	}
}

func (ix *Indexer) watchLoop() {
	for {
		ix.waitForRoot()
		watcher, err := fsnotify.NewWatcher()
		if err != nil {
			log.Printf("index: watcher unavailable: %v", err)
			time.Sleep(time.Minute)
			continue
		}
		if err := ix.addWatches(watcher, ix.root); err != nil {
			log.Printf("index: watch %s: %v", ix.root, err)
			watcher.Close()
			time.Sleep(time.Minute)
			continue
		}
		ix.watcherOK.Store(true)
		log.Printf("index: watching %s", ix.root)

	watch:
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					break watch
				}
				ix.handleEvent(watcher, event)
			case err, ok := <-watcher.Errors:
				if !ok {
					break watch
				}
				log.Printf("index: watch error: %v", err)
			}
		}
		ix.watcherOK.Store(false)
		watcher.Close()
		time.Sleep(30 * time.Second)
	}
}

func (ix *Indexer) addWatches(watcher *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(full string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		if full != root && skipIndexDir(entry.Name()) {
			return filepath.SkipDir
		}
		if err := watcher.Add(full); err != nil {
			if full == root {
				return err
			}
			log.Printf("index: cannot watch %s: %v", full, err)
		}
		return nil
	})
}

func (ix *Indexer) handleEvent(watcher *fsnotify.Watcher, event fsnotify.Event) {
	rel, err := filepath.Rel(ix.root, event.Name)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return
	}
	rel = filepath.ToSlash(rel)

	if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
		if ix.RemovePrefix(rel) {
			ix.ScheduleSave()
		}
	}
	if event.Op&fsnotify.Create != 0 {
		info, err := os.Stat(event.Name)
		if err == nil {
			if info.IsDir() {
				if err := ix.addWatches(watcher, event.Name); err == nil {
					go ix.indexDir(event.Name)
				}
			} else if isImage(rel) {
				ix.scheduleHash(rel, hashReadyDelay)
			}
		}
	}
	if event.Op&fsnotify.Write != 0 && isImage(rel) {
		ix.scheduleHash(rel, hashReadyDelay)
	}
}

func (ix *Indexer) indexDir(dir string) {
	time.Sleep(time.Second)
	count := 0
	_ = filepath.WalkDir(dir, func(full string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !isImage(entry.Name()) {
			return nil
		}
		rel, err := filepath.Rel(ix.root, full)
		if err != nil {
			return nil
		}
		ix.scheduleHash(filepath.ToSlash(rel), hashReadyDelay)
		count++
		if count%50 == 0 {
			time.Sleep(20 * time.Millisecond)
		}
		return nil
	})
}

func (ix *Indexer) scheduleHash(path string, delay time.Duration) {
	ix.pendingMu.Lock()
	ix.pending[path] = time.Now().Add(delay)
	ix.pendingMu.Unlock()
	select {
	case ix.pendingWake <- struct{}{}:
	default:
	}
}

func (ix *Indexer) pendingLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ix.pendingWake:
		case <-ticker.C:
		}
		now := time.Now()
		due := make([]string, 0)
		ix.pendingMu.Lock()
		for path, at := range ix.pending {
			if at.After(now) {
				continue
			}
			due = append(due, path)
			delete(ix.pending, path)
		}
		ix.pendingMu.Unlock()
		for _, path := range due {
			ix.hashAndAdd(path)
			if len(due) > 1 {
				time.Sleep(20 * time.Millisecond)
			}
		}
	}
}

func (ix *Indexer) hashAndAdd(rel string) {
	full := filepath.Join(ix.root, filepath.FromSlash(rel))
	info, err := os.Stat(full)
	if err != nil || !info.Mode().IsRegular() || !isImage(rel) {
		return
	}
	if info.Size() == 0 || time.Since(info.ModTime()) < time.Second {
		ix.scheduleHash(rel, 2*time.Second)
		return
	}
	ix.mu.Lock()
	known, ok := ix.byPath[rel]
	ix.mu.Unlock()
	if ok && known.Hash != "" && known.Size == info.Size() && known.ModTime == info.ModTime().UnixNano() {
		return
	}
	sum, err := hashFile(full)
	if err != nil {
		return
	}
	ix.mu.Lock()
	ix.AddLocked(rel, info.Size(), info.ModTime().UnixNano(), sum)
	ix.mu.Unlock()
	ix.ScheduleSave()
}

func (ix *Indexer) waitForRoot() {
	for {
		if info, err := os.Stat(ix.root); err == nil && info.IsDir() {
			return
		}
		log.Printf("index: waiting for %s", ix.root)
		time.Sleep(10 * time.Second)
	}
}

func skipIndexDir(name string) bool {
	return strings.HasPrefix(name, ".") ||
		name == "$RECYCLE.BIN" ||
		name == "System Volume Information" ||
		name == "@eaDir" ||
		name == "#recycle"
}

func (ix *Indexer) pruneSeen(seen map[string]indexRecord) {
	count := 0
	for path, record := range seen {
		full := filepath.Join(ix.root, filepath.FromSlash(path))
		info, err := os.Stat(full)
		if err != nil || !info.Mode().IsRegular() || info.Size() != record.Size || info.ModTime().UnixNano() != record.ModTime {
			delete(seen, path)
		}
		count++
		if count%200 == 0 {
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func hashFile(full string) (string, error) {
	file, err := os.Open(full)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func validIndexRecord(record indexRecord) bool {
	if len(record.Hash) != 64 || record.Path == "" || record.Size <= 0 {
		return false
	}
	if strings.Contains(record.Path, "\\") || strings.HasPrefix(record.Path, "/") {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(record.Path)))
	if clean != record.Path || record.Path == "." || strings.HasPrefix(record.Path, "../") {
		return false
	}
	return true
}

func (ix *Indexer) String() string {
	return fmt.Sprintf("index(%d files)", ix.Count())
}
