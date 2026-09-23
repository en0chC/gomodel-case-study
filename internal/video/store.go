// Package videostore defines persistence for the gateway-ID <-> backend-ID
// mapping, mirroring internal/batch/store.go's shape exactly.
package videostore
 
import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
 
	"github.com/enterpilot/gomodel/internal/core"
)
 
// ErrNotFound mirrors batch's ErrNotFound (name only confirmed by usage in
// store.go's List doc comment — the actual sentinel wasn't shown, so this is
// a same-shaped stand-in; swap for the real one if it lives elsewhere).
var ErrNotFound = errors.New("videostore: not found")
 
// StoredVideo is the persisted record for one video job: the response
// GoModel hands back to callers (bearing the gateway-minted ID), plus which
// backend actually owns it and what that backend calls it — the "Backend job
// ID <-> GoModel video ID" mapping from the Flows doc.
type StoredVideo struct {
	Video           *core.VideoResponse
	ProviderType    string
	ProviderVideoID string
	RequestID       string
	UserPath        string
	SessionID       string
}
 
// Store defines persistence operations for video job lifecycle, mirroring
// batch.Store's shape (same method signatures, same List semantics).
type Store interface {
	Create(ctx context.Context, video *StoredVideo) error
	Get(ctx context.Context, id string) (*StoredVideo, error)
	// List returns jobs newest first, starting after the cursor id. A
	// non-empty userPath keeps only jobs inside that subtree, matching
	// batch.Store.List's documented behavior.
	List(ctx context.Context, limit int, after, userPath string) ([]*StoredVideo, error)
	Update(ctx context.Context, video *StoredVideo) error
	Delete(ctx context.Context, id string) error
	Close() error
}
 
// MemoryStore is an in-process Store: fine for getting the pipeline working
// and testable right now, but state doesn't survive a restart — same caveat
// the mock backend itself carries. Your actual batchStore is presumably
// sqlite-backed (per "storage configured","type":"sqlite" in the startup
// log), so this should eventually be swapped for whatever backs that, for
// the same durability. Not attempted here since that implementation wasn't
// shown to me — this is a working stand-in, not a guess at sqlite schema.
type MemoryStore struct {
	mu    sync.RWMutex
	data  map[string]*StoredVideo
	order []string // newest-first order of IDs, for List
}
 
// NewMemoryStore creates an empty in-memory video store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{data: make(map[string]*StoredVideo)}
}
 
func (s *MemoryStore) Create(_ context.Context, video *StoredVideo) error {
	if video == nil || video.Video == nil || video.Video.ID == "" {
		return errors.New("videostore: video and video.ID are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data[video.Video.ID]; exists {
		return fmt.Errorf("videostore: video %s already exists", video.Video.ID)
	}
	s.data[video.Video.ID] = video
	s.order = append([]string{video.Video.ID}, s.order...)
	return nil
}
 
func (s *MemoryStore) Get(_ context.Context, id string) (*StoredVideo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.data[id]
	if !ok {
		return nil, ErrNotFound
	}
	return v, nil
}
 
func (s *MemoryStore) List(_ context.Context, limit int, after, userPath string) ([]*StoredVideo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
 
	start := 0
	if after != "" {
		found := false
		for i, id := range s.order {
			if id == after {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			return nil, ErrNotFound
		}
	}
 
	var out []*StoredVideo
	for _, id := range s.order[start:] {
		v := s.data[id]
		if userPath != "" && !strings.HasPrefix(v.UserPath, userPath) {
			continue
		}
		out = append(out, v)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}
 
func (s *MemoryStore) Update(_ context.Context, video *StoredVideo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[video.Video.ID]; !ok {
		return ErrNotFound
	}
	s.data[video.Video.ID] = video
	return nil
}
 
func (s *MemoryStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[id]; !ok {
		return ErrNotFound
	}
	delete(s.data, id)
	for i, oid := range s.order {
		if oid == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	return nil
}
 
func (s *MemoryStore) Close() error { return nil }