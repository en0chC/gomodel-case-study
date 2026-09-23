// Local disk cache for finished video bytes
package gateway
 
import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Directory path
type videoFileStore struct {
	dir string
}

// Constructor for videoFileStore
func newVideoFileStore(dir string) *videoFileStore {
	// If no directory is provided, use default "data/videos"
	if dir == "" {
		dir = "data/videos"
	}
	return &videoFileStore{dir: dir}
}

// Returns full path for given video ID 
func (s *videoFileStore) path(id string) string {
	return filepath.Join(s.dir, id+".mp4")
}

// Reads stream and writes it to disk for given video ID
func (s *videoFileStore) Save(id string, src io.ReadCloser) error {
	defer src.Close()
	// Create directory if it doesn't exist
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("gateway: create video content dir: %w", err)
	}
	// Create file for writing video content
	dst, err := os.Create(s.path(id))
	if err != nil {
		return fmt.Errorf("gateway: create video content file for %s: %w", id, err)
	}
	defer dst.Close()
	// Copy content from source stream to destination file
	if _, err := io.Copy(dst, src); err != nil {
		os.Remove(s.path(id))
		return fmt.Errorf("gateway: write video content for %s: %w", id, err)
	}
	return nil
}

// Opens locally cached video content for given ID
func (s *videoFileStore) Open(id string) (io.ReadCloser, error) {
	// Open file from disk
	f, err := os.Open(s.path(id))
	if err != nil {
		// File doesn't exist
		if errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("gateway: video content not found")
		}
		// Other errors
		return nil, fmt.Errorf("gateway: open video content for %s: %w", id, err)
	}
	return f, nil
}

// Checks if video content exists for given ID
func (s *videoFileStore) Exists(id string) bool {
	_, err := os.Stat(s.path(id))
	return err == nil
}

// Deletes locally cached video content for given ID
func (s *videoFileStore) Delete(id string) error {
	if err := os.Remove(s.path(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("gateway: delete video content for %s: %w", id, err)
	}
	return nil
}