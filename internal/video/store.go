// Defines video job persistence operations
package videostore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/enterpilot/gomodel/internal/core"
)

// Returned when a video job is not found in the store
var ErrNotFound = errors.New("videostore: not found")

// Record persisted per job, gateway ID <-> provider ID mapping
type StoredVideo struct {
	Video           *core.VideoResponse
	ProviderType    string
	ProviderVideoID string
	RequestID       string
	UserPath        string
	SessionID       string
}

// Defines the interface for video job persistence operations
type Store interface {
	Create(ctx context.Context, video *StoredVideo) error
	Get(ctx context.Context, id string) (*StoredVideo, error)
	List(ctx context.Context, limit int, after, userPath string) ([]*StoredVideo, error)
	Update(ctx context.Context, video *StoredVideo) error
	Delete(ctx context.Context, id string) error
	Close() error
}

// In-memory implementation of the store interface
type MemoryStore struct {
	mu    sync.RWMutex
	data  map[string]*StoredVideo
	order []string // newest-first order of IDs, for List
}

// NewMemoryStore creates an empty in-memory video store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{data: make(map[string]*StoredVideo)}
}

// Adds new video job to the store
func (s *MemoryStore) Create(_ context.Context, video *StoredVideo) error {
	// Validate input
	if video == nil || video.Video == nil || video.Video.ID == "" {
		return errors.New("videostore: video and video.ID are required")
	}
	// Lock for writing
	s.mu.Lock()
	defer s.mu.Unlock()
	// Check for duplicate ID
	if _, exists := s.data[video.Video.ID]; exists {
		return fmt.Errorf("videostore: video %s already exists", video.Video.ID)
	}
	// Add job to store
	s.data[video.Video.ID] = video
	s.order = append([]string{video.Video.ID}, s.order...)
	return nil
}

// Retrieves video job by ID
func (s *MemoryStore) Get(_ context.Context, id string) (*StoredVideo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.data[id]
	if !ok {
		return nil, ErrNotFound
	}
	return v, nil
}

// Lists video jobs
func (s *MemoryStore) List(_ context.Context, limit int, after, userPath string) ([]*StoredVideo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	start := 0
	// If 'after' is specified, only list jobs after that ID
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
	// Iterate over ordered IDs and collect jobs
	for _, id := range s.order[start:] {
		v := s.data[id]
		// Filter by userPath if specified
		if userPath != "" && !strings.HasPrefix(v.UserPath, userPath) {
			continue
		}
		out = append(out, v)
		// Stop if limit is reached
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// Updates an existing video job in the store
func (s *MemoryStore) Update(_ context.Context, video *StoredVideo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[video.Video.ID]; !ok {
		return ErrNotFound
	}
	s.data[video.Video.ID] = video
	return nil
}

// Deletes a video job from the store
func (s *MemoryStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[id]; !ok {
		return ErrNotFound
	}
	delete(s.data, id)
	for i, oid := range s.order {
		if oid == id {
			// Remove ID from order slice
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	return nil
}

// Close releases resources (no-op for memory store)
func (s *MemoryStore) Close() error { return nil }
