package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/wyw14/solo-039/internal/materialbalance"
)

// statusConflictErrors 是与窗口状态相关、应映射为 409 的读数错误；其余读数错误按 400 处理。
var statusConflictErrors = []error{
	materialbalance.ErrBeforeFirstWindow,
	materialbalance.ErrAfterLastWindow,
	materialbalance.ErrWindowAlreadyOpen,
}

func (s *Server) postMaterialReading(w http.ResponseWriter, r *http.Request) {
	var input materialReadingRequest
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	ack, err := s.plant.AddMaterialReading(input.Point, input.Value, input.At)
	if err != nil {
		status := http.StatusBadRequest
		for _, sentinel := range statusConflictErrors {
			if errors.Is(err, sentinel) {
				status = http.StatusConflict
				break
			}
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":   "accepted",
		"sequence": ack.Sequence,
		"revised":  ack.Revised,
		"settled":  ack.Settled,
	})
}

func (s *Server) getMaterialBalance(w http.ResponseWriter, r *http.Request) {
	var view materialbalance.WindowView
	var ok bool
	if raw := r.URL.Query().Get("window"); raw != "" {
		seq, err := strconv.Atoi(raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid window sequence"})
			return
		}
		view, ok = s.plant.MaterialWindow(seq)
	} else {
		view, ok = s.plant.LatestMaterialWindow()
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no material balance window"})
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) getMaterialWindows(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"windows":   s.plant.MaterialWindows(),
		"revisions": s.plant.MaterialRevisionCount(),
	})
}
