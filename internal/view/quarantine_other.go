//go:build !darwin

package view

import "os"

// markDownloaded does nothing outside macOS, which has no quarantine mark.
func markDownloaded(*os.File) error { return nil }
