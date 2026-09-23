// Goes in internal/gateway/video_files.go, next to video_orchestrator.go.
//
// Deliberately separate from videostore.Store: that one holds job metadata
// (status, the gateway-ID <-> backend-ID mapping); this one holds the raw
// mp4 bytes on local disk. Same split filestore/batchstore already have
// between metadata and content elsewhere in the codebase.
package gateway
 
import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ErrVideoContentNotFound means no content has been saved locally for a
// given video ID yet.
var ErrVideoContentNotFound = errors.New("gateway: video content not found")
 
// defaultVideoContentDir sits next to the sqlite db ("data/gomodel.db" per
// storage configured in the startup log), so the same "mount a volume here"
// advice that already applies to the db applies to this too.
const defaultVideoContentDir = "data/videos"

// videoFileStore persists video content bytes to local disk, keyed by the
// gateway-minted video ID (not the provider's own ID — this survives the job
// being re-fetched under whichever provider owns it).
type videoFileStore struct {
	dir string
}

// newVideoFileStore returns a store rooted at dir, or defaultVideoContentDir
// if dir is empty. The directory itself is created lazily on first Save,
// not here — keeps this constructor error-free, matching NewBatchOrchestrator's
// shape (no error return either).
func newVideoFileStore(dir string) *videoFileStore {
	if dir == "" {
		dir = defaultVideoContentDir
	}
	return &videoFileStore{dir: dir}
}
 
func (s *videoFileStore) path(id string) string {
	return filepath.Join(s.dir, id+".mp4")
}

// Save reads src to completion and writes it to local disk under id,
// closing src either way. A partial write (disk full, etc.) is cleaned up
// rather than left behind as a corrupt file that Exists would later report
// as present.
func (s *videoFileStore) Save(id string, src io.ReadCloser) error {
	defer src.Close()
 
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("gateway: create video content dir: %w", err)
	}
 
	dst, err := os.Create(s.path(id))
	if err != nil {
		return fmt.Errorf("gateway: create video content file for %s: %w", id, err)
	}
	defer dst.Close()
 
	if _, err := io.Copy(dst, src); err != nil {
		os.Remove(s.path(id))
		return fmt.Errorf("gateway: write video content for %s: %w", id, err)
	}
	return nil
}

// Open returns a reader over the locally stored content for id.
// ErrVideoContentNotFound if nothing has been saved yet.
func (s *videoFileStore) Open(id string) (io.ReadCloser, error) {
	f, err := os.Open(s.path(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrVideoContentNotFound
		}
		return nil, fmt.Errorf("gateway: open video content for %s: %w", id, err)
	}
	return f, nil
}

// Exists reports whether content has already been saved locally for id.
func (s *videoFileStore) Exists(id string) bool {
	_, err := os.Stat(s.path(id))
	return err == nil
}

// Delete removes the locally stored content for id. Not an error if nothing
// was ever saved — deleting a job that never finished (so never had content
// pulled down) is a normal case, not a failure.
func (s *videoFileStore) Delete(id string) error {
	if err := os.Remove(s.path(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("gateway: delete video content for %s: %w", id, err)
	}
	return nil
}