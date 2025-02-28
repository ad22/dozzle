package web

import (
	"bufio"
	"io/ioutil"
	"context"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"

	"time"

	"github.com/dustin/go-humanize"

	log "github.com/sirupsen/logrus"
)

func (h *handler) fetchLogsBetweenDates(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=UTF-8")

	from, _ := time.Parse(time.RFC3339, r.URL.Query().Get("from"))
	to, _ := time.Parse(time.RFC3339, r.URL.Query().Get("to"))
	id := r.URL.Query().Get("id")

	reader, err := h.client.ContainerLogsBetweenDates(r.Context(), id, from, to)
	defer reader.Close()

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	io.Copy(w, reader)
}

func (h *handler) downloadLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=UTF-8")
	id := r.URL.Query().Get("id")
	container, err := h.client.FindContainer(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	reader, err := h.client.ContainerLogsFull(r.Context(), container.ID)
	defer reader.Close()

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	filename := container.ID + ".log"

	tmpFile, err := ioutil.TempFile("", filename)
        if err != nil {
	    log.Fatal("Could not create temporary file", err)
	}
	defer tmpFile.Close()
	io.Copy(tmpFile, reader)
	http.ServeContent(w, r, filename, time.Time{}, tmpFile)
}

func (h *handler) streamLogs(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}

	f, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
		return
	}

	container, err := h.client.FindContainer(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	lastEventId := r.Header.Get("Last-Event-ID")
	if len(r.URL.Query().Get("lastEventId")) > 0 {
		lastEventId = r.URL.Query().Get("lastEventId")
	}

	reader, err := h.client.ContainerLogs(r.Context(), container.ID, h.config.TailSize, lastEventId)
	if err != nil {
		if err == io.EOF {
			fmt.Fprintf(w, "event: container-stopped\ndata: end of stream\n\n")
			f.Flush()
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	defer reader.Close()

	buffered := bufio.NewReader(reader)
	var readerError error
	var message string
	for {
		message, readerError = buffered.ReadString('\n')
		fmt.Fprintf(w, "data: %s\n", message)
		if index := strings.IndexAny(message, " "); index != -1 {
			id := message[:index]
			if _, err := time.Parse(time.RFC3339Nano, id); err == nil {
				fmt.Fprintf(w, "id: %s\n", id)
			}
		}
		fmt.Fprintf(w, "\n")
		f.Flush()
		if readerError != nil {
			break
		}
	}

	log.Debugf("streaming stopped: %v", container.ID)

	if readerError == io.EOF {
		log.Debugf("container stopped: %v", container.ID)
		fmt.Fprintf(w, "event: container-stopped\ndata: end of stream\n\n")
		f.Flush()
	} else if readerError != context.Canceled {
		log.Errorf("unknown error while streaming %v", readerError.Error())
	}

	log.WithField("routines", runtime.NumGoroutine()).Debug("runtime goroutine stats")

	if log.IsLevelEnabled(log.DebugLevel) {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		// For info on each, see: https://golang.org/pkg/runtime/#MemStats
		log.WithFields(log.Fields{
			"allocated":      humanize.Bytes(m.Alloc),
			"totalAllocated": humanize.Bytes(m.TotalAlloc),
			"system":         humanize.Bytes(m.Sys),
		}).Debug("runtime mem stats")
	}
}
