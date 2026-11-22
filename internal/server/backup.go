package server

import (
	"net/http"

	"github.com/bytebanger-nl/homeenergywatch/internal/backup"
)

// getBackups returns the backup settings, the latest attempt and the
// backups. The backups themselves are never served: they hold secret.key
// and every password.
func (s *Server) getBackups(w http.ResponseWriter, r *http.Request) {
	if s.Backups == nil {
		writeJSON(w, http.StatusOK, backup.Status{Backups: []backup.Info{}})
		return
	}
	writeJSON(w, http.StatusOK, s.Backups.Status())
}

// makeBackup makes a backup now.
func (s *Server) makeBackup(w http.ResponseWriter, r *http.Request) {
	if !s.Backups.Enabled() {
		writeError(w, http.StatusConflict, "back-ups staan uit (HWLOCAL_BACKUP_DIR=off)")
		return
	}
	if _, err := s.Backups.Make(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.Backups.Status())
}
