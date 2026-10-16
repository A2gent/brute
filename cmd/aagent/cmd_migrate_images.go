package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/A2gent/brute/internal/storage"
	"github.com/spf13/cobra"
)

func newMigrateImagesCommand() *cobra.Command {
	var dataDir, backup string
	var stopped bool
	home, _ := os.UserHomeDir()
	cmd := &cobra.Command{
		Use:   "migrate-images",
		Short: "Back up SQLite, extract legacy image attachments, and VACUUM (offline)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !stopped {
				return fmt.Errorf("stop Brute and its auto-reloader first, then pass --server-stopped")
			}
			if backup == "" {
				backup = filepath.Join(dataDir, "aagent.before-images-"+time.Now().UTC().Format("20060102T150405.000000000Z")+".db")
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Backup: %s\n", backup)
			result, err := storage.MigrateSessionImages(dataDir, backup)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Migrated %d messages and %d queued messages; database %d -> %d bytes. Keep the backup until images are verified in Caesar.\n", result.Messages, result.PendingMessages, result.BeforeBytes, result.AfterBytes)
			return nil
		},
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", filepath.Join(home, ".local", "share", "aagent"), "Directory containing aagent.db and sessions/")
	cmd.Flags().StringVar(&backup, "backup", "", "New SQLite backup path (must not exist)")
	cmd.Flags().BoolVar(&stopped, "server-stopped", false, "Confirm Brute and its auto-reloader are stopped")
	return cmd
}
