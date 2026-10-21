package api

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ShivaliWaghmare/sentinel/server/internal/store"
)

const (
	maxNameLen = 100
	maxSlugLen = 50
)

type appJSON struct {
	ID               int64  `json:"id"`
	Name             string `json:"name"`
	Slug             string `json:"slug"`
	BaseURL          string `json:"base_url"`
	Notes            string `json:"notes"`
	AllowDestructive bool   `json:"allow_destructive"`
	CreatedAt        string `json:"created_at"`
}

func toAppJSON(a store.App) appJSON {
	return appJSON{
		ID:               a.ID,
		Name:             a.Name,
		Slug:             a.Slug,
		BaseURL:          a.BaseURL,
		Notes:            a.Notes,
		AllowDestructive: a.AllowDestructive,
		CreatedAt:        formatTime(a.CreatedAt),
	}
}

type createAppRequest struct {
	Name             string `json:"name"`
	BaseURL          string `json:"base_url"`
	Notes            string `json:"notes"`
	AllowDestructive bool   `json:"allow_destructive"`
}

// updateAppRequest uses pointers so absent fields stay unchanged.
// There is no slug field: sending one is rejected as an unknown field.
type updateAppRequest struct {
	Name             *string `json:"name"`
	BaseURL          *string `json:"base_url"`
	Notes            *string `json:"notes"`
	AllowDestructive *bool   `json:"allow_destructive"`
}

func (s *Server) handleListApps(w http.ResponseWriter, r *http.Request) {
	apps, err := s.store.ListApps(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]appJSON, 0, len(apps))
	for _, a := range apps {
		out = append(out, toAppJSON(a))
	}
	writeJSON(w, http.StatusOK, map[string][]appJSON{"apps": out})
}

// handleCreateApp validates the input, picks a free slug, stores the app and
// creates agents/tests/<slug>/{manual,generated}.
func (s *Server) handleCreateApp(w http.ResponseWriter, r *http.Request) {
	var req createAppRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeRequestError(w, err)
		return
	}
	name, err := validateName(req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	baseURL, err := validateBaseURL(req.BaseURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	base := slugify(name)
	if base == "" {
		writeError(w, http.StatusBadRequest, "name must contain at least one letter or digit")
		return
	}

	slug, err := s.freeSlug(r.Context(), base)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	app, err := s.store.CreateApp(r.Context(), store.NewApp{
		Name:             name,
		Slug:             slug,
		BaseURL:          baseURL,
		Notes:            req.Notes,
		AllowDestructive: req.AllowDestructive,
	})
	if errors.Is(err, store.ErrConflict) {
		// Only possible if another request took the slug a moment ago.
		writeError(w, http.StatusConflict, fmt.Sprintf("slug %q was just taken; please retry", slug))
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	if err := s.createTestDirs(slug); err != nil {
		// Undo the insert so the app and its folders never disagree.
		if delErr := s.store.DeleteApp(r.Context(), app.ID); delErr != nil {
			err = errors.Join(err, delErr)
		}
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toAppJSON(app))
}

func (s *Server) handleGetApp(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "app not found")
		return
	}
	app, err := s.store.GetApp(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "app not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAppJSON(app))
}

func (s *Server) handleUpdateApp(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "app not found")
		return
	}
	var req updateAppRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeRequestError(w, err)
		return
	}
	if req.Name == nil && req.BaseURL == nil && req.Notes == nil && req.AllowDestructive == nil {
		writeError(w, http.StatusBadRequest, "no fields to update; send any of name, base_url, notes, allow_destructive")
		return
	}

	update := store.AppUpdate{Notes: req.Notes, AllowDestructive: req.AllowDestructive}
	if req.Name != nil {
		name, err := validateName(*req.Name)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		update.Name = &name
	}
	if req.BaseURL != nil {
		baseURL, err := validateBaseURL(*req.BaseURL)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		update.BaseURL = &baseURL
	}

	app, err := s.store.UpdateApp(r.Context(), id, update)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "app not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAppJSON(app))
}

type deleteAppResponse struct {
	Deleted bool   `json:"deleted"`
	ID      int64  `json:"id"`
	Message string `json:"message"`
}

// handleDeleteApp removes the app's rows (the database cascades) but leaves
// its test files on disk, because they may be hand-written work.
func (s *Server) handleDeleteApp(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "app not found")
		return
	}
	app, err := s.store.GetApp(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "app not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	err = s.store.DeleteApp(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "app not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, deleteAppResponse{
		Deleted: true,
		ID:      id,
		Message: fmt.Sprintf("App %q and its database rows were deleted. Test files in agents/tests/%s/ were left on disk.", app.Name, app.Slug),
	})
}

// validateName trims the name and checks it is present and not too long.
func validateName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", errors.New("name is required")
	}
	if len([]rune(name)) > maxNameLen {
		return "", fmt.Errorf("name must be at most %d characters", maxNameLen)
	}
	return name, nil
}

// validateBaseURL requires an absolute http or https URL with a host.
func validateBaseURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("base_url is required")
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("base_url must be an absolute http(s) URL, e.g. http://localhost:4000")
	}
	return s, nil
}

// slugify lowercases ASCII letters and digits and joins everything else into
// single dashes: "ShopLite (staging)!" → "shoplite-staging". Non-ASCII
// characters count as separators, so slugs are always safe folder names.
func slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		} else {
			dash = true
		}
	}
	slug := b.String()
	if len(slug) > maxSlugLen {
		slug = strings.TrimRight(slug[:maxSlugLen], "-")
	}
	return slug
}

// freeSlug returns base, or base-2, base-3, … for the first slug that no app
// uses and that has no folder under agents/tests. Checking the folder matters
// because deleting an app leaves its files: a new app must not silently
// inherit an old app's tests.
func (s *Server) freeSlug(ctx context.Context, base string) (string, error) {
	for n := 1; n < 1000; n++ {
		slug := base
		if n > 1 {
			slug = base + "-" + strconv.Itoa(n)
		}
		taken, err := s.store.SlugExists(ctx, slug)
		if err != nil {
			return "", err
		}
		if taken {
			continue
		}
		_, err = os.Stat(s.testsDir(slug))
		if errors.Is(err, fs.ErrNotExist) {
			return slug, nil
		}
		if err != nil {
			return "", fmt.Errorf("check tests dir: %w", err)
		}
	}
	return "", fmt.Errorf("no free slug for %q", base)
}

// testsDir is AGENTS_DIR/tests/<slug>.
func (s *Server) testsDir(slug string) string {
	return filepath.Join(s.cfg.AgentsDir, "tests", slug)
}

func (s *Server) createTestDirs(slug string) error {
	for _, sub := range []string{store.OriginManual, store.OriginGenerated} {
		if err := os.MkdirAll(filepath.Join(s.testsDir(slug), sub), 0o755); err != nil {
			return fmt.Errorf("create tests dir: %w", err)
		}
	}
	return nil
}
