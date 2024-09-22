package hub

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/Dreamacro/clash/tunnel"
	"github.com/go-chi/chi"
	"github.com/go-chi/render"
	log "github.com/sirupsen/logrus"
)

var (
	tun = tunnel.GetInstance()
)

type Traffic struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

type Error struct {
	Message string `json:"message"`
}

func NewHub(addr string) {
	r := chi.NewRouter()

	r.Get("/traffic", trafficHandler)
	r.Get("/logs", logsHandler)
	r.Mount("/configs", configRouter())

	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

func trafficHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	tick := time.NewTicker(time.Second)
	defer tick.Stop()

	t := tun.Traffic()
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	for range tick.C {
		up, down := t.Now()
		if err := json.NewEncoder(w).Encode(Traffic{Up: up, Down: down}); err != nil {
			log.Errorf("Error encoding traffic data: %v", err)
			break
		}
		flusher.Flush()
	}
}

type LogRequest struct {
	Level string `json:"level"`
}

type LogEntry struct {
	Type    string `json:"type"`
	Payload string `json:"payload"`
}

func logsHandler(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	req := &LogRequest{}
	if err := render.DecodeJSON(r.Body, req); err != nil {
		http.Error(w, "Invalid request payload", http.StatusBadRequest)
		return
	}

	if req.Level == "" {
		req.Level = "info"
	}

	levelMapping := map[string]tunnel.LogLevel{
		"info":    tunnel.INFO,
		"debug":   tunnel.DEBUG,
		"error":   tunnel.ERROR,
		"warning": tunnel.WARNING,
	}

	level, ok := levelMapping[req.Level]
	if !ok {
		http.Error(w, "Invalid log level", http.StatusBadRequest)
		return
	}

	src := tun.Log()
	sub, err := src.Subscribe()
	if err != nil {
		http.Error(w, "Unable to subscribe to logs", http.StatusInternalServerError)
		return
	}
	defer src.UnSubscribe(sub)

	w.Header().Set("Content-Type", "application/json")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	for elm := range sub {
		log := elm.(tunnel.Log)
		if log.LogLevel > level {
			continue
		}

		if err := json.NewEncoder(w).Encode(LogEntry{
			Type:    log.Type(),
			Payload: log.Payload,
		}); err != nil {
			log.Errorf("Error encoding log entry: %v", err)
			break
		}
		flusher.Flush()
	}
}
