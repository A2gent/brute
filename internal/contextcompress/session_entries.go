package contextcompress

import "github.com/A2gent/brute/internal/session"

// MergeSessionEntries unions originals by hash without replacing unrelated
// metadata or dropping entries produced by another compression phase.
func MergeSessionEntries(dst, src *session.Session) {
	entries := readSessionEntries(dst.Metadata)
	for hash, value := range readSessionEntries(src.Metadata) {
		entries[hash] = value
	}
	if len(entries) == 0 {
		return
	}
	if dst.Metadata == nil {
		dst.Metadata = make(map[string]interface{})
	}
	dst.Metadata[sessionCCRMetadataKey] = encodeSessionEntries(entries)
}

// SyncSessionEntries keeps the active turn aligned with entries persisted by
// request-time compression before any subsequent session Save can overwrite them.
func (c *Compressor) SyncSessionEntries(sess *session.Session) {
	if c.sessionStore == nil {
		return
	}
	fresh, err := c.sessionStore.Get(sess.ID)
	if err == nil && fresh != nil {
		MergeSessionEntries(sess, fresh)
	}
}
