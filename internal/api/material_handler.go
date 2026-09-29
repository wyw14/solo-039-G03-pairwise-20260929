package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

type materialReadingRequest struct {
	Point string  `json:"point"`
	Value float64 `json:"value"`
	At    int64   `json:"at"`
}

type materialCorrectionRequest struct {
	Value  float64 `json:"value"`
	Reason string  `json:"reason"`
}

func (s *Server) recordMaterial(w http.ResponseWriter, r *http.Request) {
	var input materialReadingRequest
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if input.At == 0 {
		input.At = time.Now().Unix()
	}
	reading, windows, err := s.plant.RecordMaterialReading(input.Point, input.Value, input.At)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"seq": reading.Seq, "point": reading.Point, "at": reading.At, "windows": windows})
}

func (s *Server) correctMaterial(w http.ResponseWriter, r *http.Request) {
	seq, err := strconv.ParseInt(chi.URLParam(r, "seq"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid reading sequence"})
		return
	}
	var input materialCorrectionRequest
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	affected, err := s.plant.CorrectMaterialReading(seq, input.Value, input.Reason)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"seq": seq, "affected_windows": affected})
}

func (s *Server) materialBalance(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"windows": s.plant.MaterialBalances()})
}
