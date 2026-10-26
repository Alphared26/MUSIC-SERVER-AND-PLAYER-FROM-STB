package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"flac-server/internal/db"
)

// Scan state. One scan at a time: a second request while a scan is running
// returns the running job instead of starting a competing walk.
type scanState struct {
	mu      sync.Mutex
	running bool
	jobID   string
}

func (h *Handler) startScan(reason string) string {
	h.scan.mu.Lock()
	if h.scan.running {
		id := h.scan.jobID
		h.scan.mu.Unlock()
		return id
	}
	id := randomToken(8)
	h.scan.running = true
	h.scan.jobID = id
	h.scan.mu.Unlock()

	if err := h.DB.CreateScanJob(id); err != nil {
		h.Log.Error("create scan job", "err", err)
	}
	go func() {
		res, err := h.Scanner.Scan()
		status := "completed"
		var added, updated, removed int
		if err != nil {
			status = "failed"
			h.Log.Error("scan failed", "reason", reason, "err", err)
		} else {
			added, updated, removed = res.Added, res.Updated, res.Removed
			h.Log.Info("scan done", "reason", reason, "scanned", res.FilesScanned,
				"added", added, "updated", updated, "removed", removed)
		}
		if err := h.DB.UpdateScanJob(id, status, added, updated, removed, err); err != nil {
			h.Log.Error("update scan job", "err", err)
		}
		h.scan.mu.Lock()
		h.scan.running = false
		h.scan.mu.Unlock()
	}()
	return id
}

// triggerScan starts a scan and returns the job id.
func (h *Handler) triggerScan(w http.ResponseWriter, r *http.Request) {
	id := h.startScan("api")
	job, err := h.DB.GetScanJob(id)
	if err != nil {
		// The job row may not be visible yet on a very slow disk; the id is
		// still returned so the client can poll.
		h.json(w, http.StatusAccepted, ScanJob{ID: id, Status: "pending", StartedAt: time.Now().Unix()})
		return
	}
	h.json(w, http.StatusAccepted, toScanJobDTO(*job))
}

// scanStatus reports progress for one scan job.
func (h *Handler) scanStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	job, err := h.DB.GetScanJob(id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) || errors.Is(err, errNoRow) {
			h.fail(w, http.StatusNotFound, "scan job not found")
			return
		}
		h.fail(w, http.StatusInternalServerError, "get scan job: %v", err)
		return
	}
	dtoJob := toScanJobDTO(*job)
	// While a scan is running the row is not updated until the end, so report
	// the live state and let files_scanned stay at whatever was last written.
	if job.Status == "running" {
		dtoJob.Status = "running"
	}
	h.json(w, http.StatusOK, dtoJob)
}

func toScanJobDTO(j db.ScanJob) ScanJob {
	out := ScanJob{
		ID: j.ID, Status: j.Status, StartedAt: j.StartedAt,
		FilesScanned: j.FilesScanned, FilesAdded: j.FilesAdded,
		FilesUpdated: j.FilesUpdated, FilesRemoved: j.FilesRemoved,
	}
	if j.CompletedAt != nil {
		out.CompletedAt = *j.CompletedAt
	}
	if j.Error != nil {
		out.Error = *j.Error
	}
	return out
}

// scanJobID is used by tests and callers that need a stable id format.
var _ = fmt.Sprintf

var _ = hex.EncodeToString
var _ = rand.Read
