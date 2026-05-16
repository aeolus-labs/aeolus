// Package watcher reloads Aeolus when aeolus.yaml changes on disk, so that
// hand edits and external tools (git pull, MDM agents, etc.) take effect
// without restarting the proxy.
package watcher

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watch invokes onChange whenever path is modified. Events are debounced so
// a flurry of writes within `dwell` triggers a single callback. Returns
// when ctx is canceled.
//
// fsnotify only watches the file's parent directory robustly across atomic
// writes (rename + create vs. truncate+write); we filter events by base name.
func Watch(ctx context.Context, path string, dwell time.Duration, onChange func(), log *slog.Logger) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()

	dir := filepath.Dir(path)
	base := filepath.Base(path)
	if err := w.Add(dir); err != nil {
		return err
	}

	var timer *time.Timer
	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return nil

		case event, ok := <-w.Events:
			if !ok {
				return nil
			}
			if filepath.Base(event.Name) != base {
				continue
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
				continue
			}
			log.Debug("config_file_event", "op", event.Op.String())
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(dwell, onChange)

		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			log.Warn("watcher_error", "error", err.Error())
		}
	}
}
