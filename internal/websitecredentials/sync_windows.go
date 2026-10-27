package websitecredentials

// lean: Windows cannot fsync a directory handle through os.File; NTFS journals
// MoveFileEx metadata. Upgrade path: FlushFileBuffers on a backup-semantics handle.
func syncDirectory(string) error { return nil }
