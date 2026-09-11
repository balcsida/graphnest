package graphservice

import (
	"context"
	"slices"
	"strings"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"google.golang.org/protobuf/proto"
)

func sourceUnits(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r > 0xffff {
			n++
		}
	}
	return n
}

func (s *Service) exploreSources(ctx context.Context, p authn.Principal, i inspectionScope, r ExploreRequest, result *ExploreResponse, allocation exploreAllocation, required map[string]bool) error {
	buffers := make([][]InspectionSource, len(result.Files))
	bufferBytes := 0
	for index := range result.Files {
		f := &result.Files[index]
		reservation, admitted := allocation.Allowances[f.Path]
		f.Reservation = reservation
		if !admitted {
			f.Status = "allocation_cliff"
			f.Mode = "pointer"
			continue
		}
		if f.Fact == nil {
			continue
		}
		// File-only pins keep their original outline; page limits remain explicit.
		if f.Pinned && len(f.Entities) == 0 {
			page, err := i.backend.Entities(ctx, graphprotocol.EntitiesRequest{Scope: i.scope, Selector: graphprotocol.EntitySelector{Path: &f.Path}, Limit: 100})
			result.Usage.GraphQueries++
			if err != nil {
				return err
			}
			if err = sameInspectionGeneration(result.Generations, page.Generations); err != nil {
				return err
			}
			if err = i.entities(page.Entities); err != nil {
				return err
			}
			for _, e := range page.Entities {
				if e.Fact.GetPath() != f.Path {
					return ErrGraphNotReady
				}
			}
			f.Entities = page.Entities
			if page.NextCursor != "" {
				f.Boundaries = append(f.Boundaries, "outline_page")
			}
		}
		type readRange struct{ start, end int }
		ranges := []readRange{}
		// Indexed size is only a planning hint; EOF and reader limits decide
		// whether this bounded prefix really contains the whole file. UTF-8
		// can cost three bytes per UTF-16 unit, so byte size is an upper bound.
		if f.Fact.Size > 0 && f.Fact.Size <= int64(min(reservation*3, r.SourceBytes)) {
			ranges = append(ranges, readRange{1, 1})
		}
		for _, e := range f.Entities {
			loc := e.Fact.GetLocation()
			if pos := loc.GetStart(); required[e.ID] && pos != nil && pos.Line != nil {
				start, end := int(pos.GetLine())+1, int(pos.GetLine())+1
				if loc.End != nil && loc.End.Line != nil {
					end = int(loc.End.GetLine()) + 1
				}
				ranges = append(ranges, readRange{start, end})
			}
		}
		if len(ranges) == 0 {
			ranges = []readRange{{1, 1}}
		}
		attempted := map[int]bool{}
		for _, span := range ranges {
			if slices.ContainsFunc(buffers[index], func(v InspectionSource) bool { return span.start >= v.StartLine && span.end <= v.EndLine }) {
				continue
			}
			start := max(1, span.start-3)
			if attempted[start] {
				continue
			}
			attempted[start] = true
			if result.Usage.SourceReads >= 20 {
				f.Status = "source_read_limit"
				f.Boundaries = append(f.Boundaries, f.Status)
				break
			}
			if bufferBytes >= 4<<20 {
				f.Status = "source_read_bytes"
				f.Boundaries = append(f.Boundaries, f.Status)
				break
			}
			maxRead := min(1<<20, (4<<20)-bufferBytes)
			source, err := s.readInspectionSource(ctx, p, i, InspectionSource{Path: f.Path, FileErrors: f.Fact.Errors}, start, 0, &maxRead, nil)
			result.Usage.SourceReads++
			if err != nil {
				return err
			}
			if source.Content != "" {
				if strings.Count(source.Content, "\n")+1 != source.EndLine-source.StartLine+1 {
					return ErrGraphNotReady
				}
				if source.EndLine-source.StartLine >= 1000 {
					parts := strings.SplitN(source.Content, "\n", 1001)
					source.Content = strings.Join(parts[:1000], "\n")
					source.EndLine = source.StartLine + 999
					source.Status = "truncated"
				}
			}
			f.Status = source.Status
			if source.Status != "ok" {
				f.Boundaries = append(f.Boundaries, source.Status)
			}
			if len(buffers[index]) > 0 && source.Content != "" && source.BlobSHA != buffers[index][0].BlobSHA {
				return ErrGraphNotReady
			}
			bufferBytes += len(source.Content)
			if source.Content != "" {
				buffers[index] = append(buffers[index], source)
				if start > 1 {
					f.Boundaries = append(f.Boundaries, "read_window")
				}
			}
		}
	}
	// Redistribute only genuinely unspent reservations, proportionally to the
	// remaining source demand. This never borrows another file's promised floor.
	spare, totalNeed := 0, 0
	for index, f := range result.Files {
		if f.Reservation == 0 {
			continue
		}
		demand := 0
		for _, source := range buffers[index] {
			demand += sourceUnits(source.Content)
		}
		spare += max(0, f.Reservation-demand)
		totalNeed += max(0, demand-f.Reservation)
		result.Files[index].Reservation = min(f.Reservation, demand)
	}
	if spare > 0 && totalNeed > 0 {
		for index := range result.Files {
			f := &result.Files[index]
			demand := 0
			for _, source := range buffers[index] {
				demand += sourceUnits(source.Content)
			}
			need := max(0, demand-f.Reservation)
			if f.Reservation > 0 {
				f.Reservation += min(need, int(int64(spare)*int64(need)/int64(totalNeed)))
			}
		}
	}
	remainingBytes := r.SourceBytes
	for index := range result.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		f := &result.Files[index]
		if len(buffers[index]) == 0 {
			continue
		}
		adaptive := (r.Config.Adaptive == nil || *r.Config.Adaptive) && !f.Pinned && len(required) > 0
		skeleton, exact := exploreAdaptive(*f, result.Relationships, buffers[index], required, r.RequiredOccurrences)
		skeleton = adaptive && skeleton
		segments := exploreWindows(buffers[index], f.Entities, required, exact, f.Reservation, remainingBytes, skeleton)
		if len(segments) == 0 {
			f.Status = "budget_exhausted"
			f.Mode = "pointer"
			continue
		}
		f.Mode = "whole"
		f.Segments = segments
		if skeleton {
			f.Mode = "skeleton"
			if f.Required {
				f.Mode = "focused"
			}
			f.Boundaries = append(f.Boundaries, "adaptive_skeleton")
		} else if len(segments) != 1 || len(buffers[index]) != 1 || segments[0].Content != buffers[index][0].Content || buffers[index][0].StartLine != 1 || buffers[index][0].Status != "ok" {
			f.Mode = "window"
			f.Boundaries = append(f.Boundaries, "source_window")
		}
		for _, segment := range segments {
			bytes := len(segment.Content)
			units := sourceUnits(segment.Content)
			remainingBytes -= bytes
			result.Usage.SourceBytes += bytes
			result.Usage.SourceUnits += units
		}
		for _, entity := range f.Entities {
			selection := exploreSelection(entity, segments)
			f.Selections = append(f.Selections, selection)
			if selection.Status != "ok" {
				f.Boundaries = append(f.Boundaries, "entity_"+selection.Status)
			}
		}
	}
	return nil
}

func exploreAdaptive(f ExploreFile, graphs []graphprotocol.TraverseResponse, sources []InspectionSource, required map[string]bool, occurrences []string) (bool, map[string]bool) {
	sibling, definesSuper := exploreFamily(f, graphs)
	exact := map[string]bool{}
	namedUnits, hasExact, offPath := 0, false, false
	linesBySource := make([][]string, len(sources))
	for at, source := range sources {
		linesBySource[at] = strings.Split(source.Content, "\n")
	}
	for _, e := range f.Entities {
		exact[e.ID] = slices.Contains(occurrences, e.Fact.Occurrence)
		hasExact = hasExact || exact[e.ID]
		if required[e.ID] && slices.Contains([]string{"function", "method", "constructor", "component"}, e.Fact.Kind) {
			offPath = offPath || !exact[e.ID]
			for at, source := range sources {
				loc := e.Fact.GetLocation()
				if loc == nil || loc.Start == nil || loc.End == nil || loc.Start.Line == nil || loc.End.Line == nil {
					continue
				}
				from, to := int(loc.Start.GetLine())+1-source.StartLine, int(loc.End.GetLine())+1-source.StartLine
				lines := linesBySource[at]
				if from >= 0 && to >= from && to < len(lines) {
					namedUnits += sourceUnits(strings.Join(lines[from:to+1], "\n"))
					break
				}
			}
		}
	}
	// Explicit required occurrences are the retained path evidence. Named
	// off-path bodies may compete only after those occurrences are funded.
	skeleton := (sibling && (!f.Required || definesSuper) || hasExact && offPath && namedUnits > f.Reservation)
	return skeleton, exact
}

// A repeated implementation family is established from original relationship
// occurrences, not spelling heuristics. Distinct pipelines never skeletonize.
func exploreFamily(file ExploreFile, graphs []graphprotocol.TraverseResponse) (sibling, definesSuper bool) {
	families := map[string]map[string]bool{}
	own := map[string]bool{}
	for _, e := range file.Entities {
		own[e.ID] = true
	}
	for _, graph := range graphs {
		for _, e := range graph.Edges {
			if e.Fact.Kind != graphv2.EdgeKind_EDGE_KIND_IMPLEMENTS && e.Fact.Kind != graphv2.EdgeKind_EDGE_KIND_EXTENDS {
				continue
			}
			if families[e.TargetID] == nil {
				families[e.TargetID] = map[string]bool{}
			}
			families[e.TargetID][e.SourceID] = true
		}
	}
	for target, members := range families {
		if len(members) >= 3 {
			definesSuper = definesSuper || own[target]
			for id := range members {
				if own[id] {
					sibling = true
				}
			}
		}
	}
	return sibling, definesSuper
}

// Segments contain only original whole lines, including CRs and internal LF
// bytes. Elision is represented by disjoint ranges, never synthetic source.
func exploreWindows(sources []InspectionSource, entities []graphprotocol.Entity, required, exact map[string]bool, units, bytes int, skeleton bool) []InspectionSource {
	if len(sources) == 1 && !skeleton && sourceUnits(sources[0].Content) <= units && len(sources[0].Content) <= bytes {
		return sources
	}
	// Allocate across the union of original lines. Overlapping reads pay once;
	// every required body is considered before any optional body or context.
	content := map[int]string{}
	for _, source := range sources {
		for at, line := range strings.Split(source.Content, "\n") {
			content[source.StartLine+at] = line
		}
	}
	numbers := make([]int, 0, len(content))
	for n := range content {
		numbers = append(numbers, n)
	}
	slices.Sort(numbers)
	chosen := map[int]bool{}
	lineUnits := make(map[int]int, len(content))
	for n, line := range content {
		lineUnits[n] = sourceUnits(line)
	}
	ordered := slices.Clone(entities)
	slices.SortStableFunc(ordered, func(a, b graphprotocol.Entity) int {
		if exact[a.ID] != exact[b.ID] {
			if exact[a.ID] {
				return -1
			}
			return 1
		}
		if required[a.ID] != required[b.ID] {
			if required[a.ID] {
				return -1
			}
			return 1
		}
		return 0
	})
	add := func(from, to int) bool {
		if from > to || to-from >= len(content) {
			return false
		}
		costUnits, costBytes := 0, 0
		for line := from; line <= to; line++ {
			text, present := content[line]
			if !present {
				return false
			}
			if !chosen[line] {
				separator := 0
				if chosen[line-1] || line > from {
					separator++
				}
				if chosen[line+1] {
					separator++
				}
				costUnits += lineUnits[line] + separator
				costBytes += len(text) + separator
			}
		}
		if costUnits > units || costBytes > bytes {
			return false
		}
		for line := from; line <= to; line++ {
			chosen[line] = true
		}
		units -= costUnits
		bytes -= costBytes
		return true
	}
	anchors := []int{}
	for _, e := range ordered {
		loc := e.Fact.Location
		if loc == nil || loc.Start == nil || loc.Start.Line == nil {
			continue
		}
		from, to := int(loc.Start.GetLine())+1, int(loc.Start.GetLine())+1
		if loc.End != nil && loc.End.Line != nil {
			to = int(loc.End.GetLine()) + 1
		}
		if _, ok := content[from]; !ok {
			continue
		}
		anchors = append(anchors, from)
		if skeleton && !exact[e.ID] && (!required[e.ID] || !slices.Contains([]string{"function", "method", "constructor", "component"}, e.Fact.Kind)) {
			to = from
			for at := from; at < from+4; at++ {
				if strings.Contains(content[at], e.Fact.Name) {
					from, to = at, at
					break
				}
			}
		}
		if !add(from, to) {
			add(from, from)
		}
	}
	if len(anchors) == 0 && len(numbers) > 0 {
		anchors = append(anchors, numbers[0])
		add(numbers[0], numbers[0])
	}
	if !skeleton {
		// Only buffered lines are candidates, even for ranges millions of lines
		// apart. This bounds context work by the retained source, not file span.
		context := slices.Clone(numbers)
		distance := make(map[int]int, len(numbers))
		for _, line := range numbers {
			d := int(^uint(0) >> 1)
			for _, anchor := range anchors {
				delta := line - anchor
				if delta < 0 {
					delta = -delta
				}
				d = min(d, delta)
			}
			distance[line] = d
		}
		slices.SortStableFunc(context, func(a, b int) int { return distance[a] - distance[b] })
		for _, line := range context {
			if !chosen[line] {
				add(line, line)
			}
		}
	}
	result := []InspectionSource{}
	for at := 0; at < len(numbers); at++ {
		from := numbers[at]
		if !chosen[from] {
			continue
		}
		to := from
		lines := []string{content[from]}
		for at+1 < len(numbers) && numbers[at+1] == to+1 && chosen[to+1] {
			at++
			to++
			lines = append(lines, content[to])
		}
		segment := sources[0]
		segment.StartLine, segment.EndLine = from, to
		segment.Content = strings.Join(lines, "\n")
		segment.Selection, segment.Range, segment.Status = nil, nil, "ok"
		result = append(result, segment)
	}
	return result
}

func exploreSelection(e graphprotocol.Entity, segments []InspectionSource) ExploreSelection {
	result := ExploreSelection{EntityID: e.ID, Range: e.Fact.Location, Segment: -1, Status: "missing_range"}
	location := e.Fact.Location
	if location == nil || location.Start == nil || location.Start.Line == nil {
		return result
	}
	result.Status = "windowed"
	for index, segment := range segments {
		start := int(location.Start.GetLine()) + 1
		end := start
		if location.End != nil && location.End.Line != nil {
			end = int(location.End.GetLine()) + 1
		}
		if start < segment.StartLine || end > segment.EndLine {
			continue
		}
		result.Segment = index
		if location.End == nil || location.End.Line == nil || location.Start.Character == nil || location.End.Character == nil {
			result.Status = "partial_range"
			return result
		}
		a, b := proto.Clone(location.Start).(*graphv2.Position), proto.Clone(location.End).(*graphv2.Position)
		a.Line = proto.Int32(a.GetLine() - int32(segment.StartLine-1))
		b.Line = proto.Int32(b.GetLine() - int32(segment.StartLine-1))
		from, e1 := graphartifact.SourceOffset(segment.Content, a)
		to, e2 := graphartifact.SourceOffset(segment.Content, b)
		if e1 != nil || e2 != nil || to < from {
			result.Status = "invalid_range"
			return result
		}
		result.Selection = &SourceSelection{StartByte: from, EndByte: to}
		result.Status = "ok"
		return result
	}
	return result
}
