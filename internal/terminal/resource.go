package terminal

import "github.com/REPPL/ferry/internal/backup"

// Compile-time assertion that the terminal preference domains satisfy the
// backup.Resource hook (Domain/Backup/Restore), so the engine can capture
// pre-mutation state and roll back through them.
var _ backup.Resource = (*PreferenceDomain)(nil)
