package graphservice

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/graphprotocol"
)

const (
	exploreHistoryTTL          = 15 * time.Minute
	exploreHistoryEntries      = 64
	exploreHistoryPerPrincipal = 4
	exploreHistoryBytes        = 4 << 20
	exploreHistoryEntryBytes   = 256 << 10
	// Conservative fixed charges include map buckets, keys, values and headers.
	exploreHistoryBase      = 256
	exploreHistoryLineBytes = 128
	exploreHistoryLines     = (exploreHistoryEntryBytes - exploreHistoryBase) / exploreHistoryLineBytes
)

type exploreLine struct {
	File   [32]byte
	Number int
}
type exploreHistoryEntry struct {
	owner         [32]byte
	created, used time.Time
	lines         map[exploreLine][32]byte
}
type exploreHistory struct {
	// ponytail: one mutex and a scan of at most 64 entries; shard only if measured contention matters.
	mu      sync.Mutex
	entries map[[32]byte]*exploreHistoryEntry
}

func exploreSessionIdentity(p authn.Principal, session string, scope graphprotocol.Scope, uploadID int64) (key, owner [32]byte) {
	// Hash the current value, never retain a Principal or any grants as authority.
	identity, _ := json.Marshal([]any{p.Subject, p.Method, p.InstallationID})
	owner = sha256.Sum256(identity)
	data, _ := json.Marshal([]any{p, session, scope, uploadID})
	return sha256.Sum256(data), owner
}
func (h *exploreHistory) snapshot(key [32]byte, now time.Time) map[exploreLine][32]byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.expire(now)
	if entry := h.entries[key]; entry != nil {
		return maps.Clone(entry.lines)
	}
	return nil
}
func (h *exploreHistory) expire(now time.Time) {
	for key, entry := range h.entries {
		if !now.Before(entry.created.Add(exploreHistoryTTL)) {
			delete(h.entries, key)
		}
	}
}
func (h *exploreHistory) record(ctx context.Context, key, owner [32]byte, lines map[exploreLine][32]byte, now time.Time) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	h.expire(now)
	if h.entries == nil {
		h.entries = make(map[[32]byte]*exploreHistoryEntry)
	}
	entry := h.entries[key]
	if entry == nil {
		entry = &exploreHistoryEntry{owner: owner, created: now, lines: make(map[exploreLine][32]byte)}
		h.entries[key] = entry
	}
	entry.used = now
	for line, digest := range lines {
		if _, exists := entry.lines[line]; exists || len(entry.lines) < exploreHistoryLines {
			entry.lines[line] = digest
		}
	}
	for {
		bytes, owned := 0, 0
		for _, e := range h.entries {
			bytes += exploreHistoryBase + len(e.lines)*exploreHistoryLineBytes
			if e.owner == owner {
				owned++
			}
		}
		if len(h.entries) <= exploreHistoryEntries && bytes <= exploreHistoryBytes && owned <= exploreHistoryPerPrincipal {
			break
		}
		var oldest [32]byte
		var age time.Time
		for k, e := range h.entries {
			if k == key || owned > exploreHistoryPerPrincipal && e.owner != owner {
				continue
			}
			if age.IsZero() || e.used.Before(age) {
				oldest, age = k, e.used
			}
		}
		delete(h.entries, oldest)
	}
	return nil
}
func exploreSourceIdentity(s InspectionSource) [32]byte {
	data, _ := json.Marshal([]any{s.Path, s.IndexedSHA, s.BlobSHA})
	return sha256.Sum256(data)
}

// Only bytes returned by a successful domain result are admitted. The digest
// includes the exact final-line bytes: a truncated prefix cannot cover a longer
// line next time, even when their line coordinates and blob identity match.
func exploreDelivered(r ExploreResponse) map[exploreLine][32]byte {
	refused := func(status string) bool {
		return slices.Contains([]string{"unreadable", "oversized", "binary", "invalid_range", "source_unavailable"}, status)
	}
	for _, f := range r.Files {
		// A later successful read may replace Status; earlier refusals remain boundaries.
		if refused(f.Status) || slices.ContainsFunc(f.Boundaries, refused) {
			return nil
		}
	}
	lines := make(map[exploreLine][32]byte)
	for _, f := range r.Files {
		for _, s := range f.Segments {
			if s.Content == "" || s.BlobSHA == "" {
				continue
			}
			file := exploreSourceIdentity(s)
			parts := strings.Split(s.Content, "\n")
			for at := range parts {
				if len(lines) >= exploreHistoryLines {
					return lines
				}
				lines[exploreLine{file, s.StartLine + at}] = exploreLineDigest(parts, at)
			}
		}
	}
	return lines
}

// A line's LF is covered only if it was present inside the delivered segment.
// CR bytes remain in the line itself, preserving CRLF and partial-CRLF boundaries.
func exploreLineDigest(lines []string, at int) [32]byte {
	line := lines[at]
	if at < len(lines)-1 {
		line += "\n"
	}
	return sha256.Sum256([]byte(line))
}

func applyExploreHistory(r *ExploreResponse, prior map[exploreLine][32]byte) {
	if len(prior) == 0 {
		return
	}
	restore := -1
	var original ExploreFile
	for at := range r.Files {
		f := &r.Files[at]
		before := *f
		var emitted, references []InspectionSource
		for _, s := range f.Segments {
			lines := strings.Split(s.Content, "\n")
			file := exploreSourceIdentity(s)
			covered := make([]bool, len(lines))
			for start := 0; start < len(lines); {
				end := start
				for end < len(lines) && s.BlobSHA != "" && prior[exploreLine{file, s.StartLine + end}] == exploreLineDigest(lines, end) {
					end++
				}
				coveredStart := start
				// Keep the first known line after new source so the separating
				// LF stays inside the emitted whole-line segment. Its following
				// separator is already proven by this line's digest.
				if coveredStart > 0 && end > start {
					coveredStart++
				}
				// A terminal LF creates an empty split element, not another source line.
				count := end - coveredStart
				if end == len(lines) && end > start && lines[end-1] == "" {
					count--
				}
				if count >= 8 {
					for i := coveredStart; i < end; i++ {
						covered[i] = true
					}
				}
				start = max(start+1, end)
			}
			for start := 0; start < len(lines); {
				end := start + 1
				for end < len(lines) && covered[end] == covered[start] {
					end++
				}
				span := s
				span.StartLine, span.EndLine = s.StartLine+start, s.StartLine+end-1
				span.Content = strings.Join(lines[start:end], "\n")
				span.Range, span.Selection = nil, nil
				if covered[start] {
					references = append(references, span)
				} else if span.Content != "" {
					emitted = append(emitted, span)
				}
				start = end
			}
		}
		if len(references) == 0 {
			continue
		}
		if restore < 0 && len(emitted) == 0 {
			restore, original = at, before
		}
		f.Segments, f.References = emitted, references
		f.Selections = nil
		for _, e := range f.Entities {
			selection := exploreSelection(e, emitted)
			if selection.Status != "ok" {
				reference := exploreSelection(e, references)
				if reference.Status == "ok" {
					index := reference.Segment
					reference.Reference = &index
					reference.Segment = -1
					reference.Status = "already_seen"
					selection = reference
				}
			}
			f.Selections = append(f.Selections, selection)
		}
		for i := range f.References {
			f.References[i].Content = ""
			f.References[i].Status = "already_seen"
		}
		f.Mode = "session_window"
		if len(emitted) == 0 {
			f.Mode = "session_pointer"
		}
	}
	units, bytes := 0, 0
	for _, f := range r.Files {
		for _, s := range f.Segments {
			units += sourceUnits(s.Content)
			bytes += len(s.Content)
		}
	}
	if bytes == 0 && restore >= 0 {
		r.Files[restore] = original
		r.SessionRestored = true
		for _, s := range original.Segments {
			units += sourceUnits(s.Content)
			bytes += len(s.Content)
		}
	}
	r.Usage.DedupSavedUnits = r.Usage.SourceUnits - units
	r.Usage.SourceUnits, r.Usage.SourceBytes = units, bytes
}
