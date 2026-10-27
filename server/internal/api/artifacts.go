package api

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// artifactRoots are the DATA_DIR folders the artifact endpoint may serve.
// Everything else in DATA_DIR (such as sentinel.db) stays private.
var artifactRoots = map[string]bool{"runs": true, "agent-runs": true}

// artifactTypes sets the Content-Type by extension; anything else is served
// as a download (application/octet-stream).
var artifactTypes = map[string]string{
	".png":  "image/png",
	".zip":  "application/zip",
	".txt":  "text/plain; charset=utf-8",
	".log":  "text/plain; charset=utf-8",
	".md":   "text/plain; charset=utf-8",
	".json": "application/json",
}

// handleArtifact serves a file under an allowed DATA_DIR folder, for example
// /api/artifacts/runs/3/artifacts/<test>/test-failed-1.png.
func (s *Server) handleArtifact(w http.ResponseWriter, r *http.Request) {
	rel := r.PathValue("path")
	// Only clean, relative, slash-separated paths: this rejects "..",
	// ".", empty segments and absolute paths before touching the disk.
	if rel == "" || path.Clean(rel) != rel || path.IsAbs(rel) || strings.Contains(rel, `\`) ||
		rel == ".." || strings.HasPrefix(rel, "../") {
		writeError(w, http.StatusBadRequest, "invalid artifact path")
		return
	}
	top, _, _ := strings.Cut(rel, "/")
	if !artifactRoots[top] {
		writeError(w, http.StatusNotFound, "artifact not found")
		return
	}

	file, ok := resolveInside(filepath.Join(s.cfg.DataDir, top), filepath.Join(s.cfg.DataDir, filepath.FromSlash(rel)))
	if !ok {
		writeError(w, http.StatusNotFound, "artifact not found")
		return
	}
	f, err := os.Open(file)
	if err != nil {
		writeError(w, http.StatusNotFound, "artifact not found")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		writeError(w, http.StatusNotFound, "artifact not found")
		return
	}

	contentType, ok := artifactTypes[strings.ToLower(filepath.Ext(file))]
	if !ok {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

// resolveInside follows symlinks in target and reports the real path if it
// is inside root (also after following root's own symlinks).
func resolveInside(root, target string) (string, bool) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false
	}
	real, err := filepath.EvalSymlinks(target) // fails for missing files too
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(realRoot, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return real, true
}
