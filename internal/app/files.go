package app

import (
	"context"
	"fmt"
	"sync"

	"github.com/dyskette/atsume/internal/download"
	"github.com/dyskette/atsume/internal/store"
)

// fileClaims records which library files downloads in progress are about to
// write. A finished chapter's file is recorded in the store; one still being
// fetched is not, so without this two chapters sharing a number could both
// pick the same free name and the second would overwrite the first.
type fileClaims struct {
	mu   sync.Mutex
	held map[string]int64 // path -> chapter ID
}

// claimFile picks the file ch will be written to: the first of its candidate
// names that no other chapter has written or is writing. It returns the path
// and a function that gives up the claim, to call once the chapter's outcome
// is recorded.
//
// A chapter re-downloaded gets its own earlier file back, since that file is
// recorded against it, and so is rewritten rather than duplicated.
func (a *App) claimFile(ctx context.Context, ch store.Chapter, target download.Chapter) (string, func(), error) {
	c := &a.files
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.held == nil {
		c.held = map[string]int64{}
	}
	// The key behind the last candidate's hash: the URL alone is not unique
	// across series, since modules store paths such as "/manga/x/1".
	key := fmt.Sprintf("%d:%s", ch.SeriesID, ch.URL)
	for _, path := range target.Candidates(a.Cfg.LibraryDir, key) {
		if id, ok := c.held[path]; ok && id != ch.ID {
			continue
		}
		owner, err := a.Store.ChapterByFile(ctx, path)
		if err != nil {
			return "", nil, err
		}
		if owner != 0 && owner != ch.ID {
			continue
		}
		c.held[path] = ch.ID
		return path, func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.held[path] == ch.ID {
				delete(c.held, path)
			}
		}, nil
	}
	return "", nil, fmt.Errorf("every file name for %q is taken by another chapter", ch.Name)
}
