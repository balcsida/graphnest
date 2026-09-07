package postgres

import (
	"context"
	"crypto/sha256"
	"regexp"
	"slices"
	"strings"

	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"
)

var discoveryGenerated = regexp.MustCompile(`(?i)(^|/)(generated|__generated__|vendor)(/|$)|[.]pb[.]|[.]generated[.]|[.]gen[.]`)
var discoveryTestFile = regexp.MustCompile(`(?i)(^|/)(__tests__|tests?|specs?|fixtures?|examples?|icons?|i18n)(/|$)|[._](test|spec)[.]|_test[.]`)

func writeGraphDiscovery(ctx context.Context, tx pgx.Tx, id int64, a *graphv2.Artifact) error {
	usage := map[string]int{}
	files := map[string]*graphv2.File{}
	for _, f := range a.Files {
		files[f.Path] = f
	}
	usageKinds := map[graphv2.EdgeKind]bool{}
	for _, r := range graphartifact.Relationships() {
		usageKinds[r.WireKind()] = r.Name != "contains" && r.Name != "imports" && r.Name != "exports"
	}
	for _, e := range a.Edges {
		if usageKinds[e.Kind] {
			usage[e.Source]++
			if e.Target != e.Source {
				usage[e.Target]++
			}
		}
	}
	_, err := tx.CopyFrom(ctx, pgx.Identifier{"graph_v2_discovery"}, []string{"upload_id", "occurrence_key", "name", "qualified_name", "signature", "documentation", "path", "language", "kind", "name_document", "qualified_document", "signature_document", "documentation_document", "path_document", "grams", "usage_count", "generated", "ambient", "test_file"}, pgx.CopyFromSlice(len(a.Nodes), func(i int) ([]any, error) {
		n := a.Nodes[i]
		values := []string{n.Name, n.QualifiedName, n.GetSignature(), n.GetDocumentation(), n.GetPath()}
		docs := make([]string, len(values))
		for j, v := range values {
			docs[j] = graphquery.DiscoveryDocument(v)
			values[j] = graphquery.NormalizeDiscovery(v)
		}
		generated := files[n.GetPath()].GetGenerated() || discoveryGenerated.MatchString(n.GetPath())
		ambient := strings.HasSuffix(n.GetPath(), ".d.ts") && usage[n.Occurrence] == 0
		grams := graphquery.DiscoveryGrams(strings.Join(append(slices.Clone(values), n.Kind, n.Language), " "))
		key := sha256.Sum256([]byte(n.Occurrence))
		return []any{id, key[:], values[0], values[1], values[2], values[3], values[4], graphquery.NormalizeDiscovery(n.Language), n.Kind, docs[0], docs[1], docs[2], docs[3], docs[4], grams, usage[n.Occurrence], generated, ambient, discoveryTestFile.MatchString(n.GetPath())}, nil
	}))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "update graph_uploads set discovery_version=1 where id=$1", id)
	return err
}

// RebuildGraphDiscovery is an explicit offline maintenance operation. It may
// load a full validated artifact; the request path never calls it. Immutable
// generation identity prevents a replacement from changing the facts rebuilt.
func (s *Store) RebuildGraphDiscovery(ctx context.Context, repositoryID, uploadID int64) error {
	a, err := s.LoadGraphV2(ctx, repositoryID, uploadID)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id int64
	if err = tx.QueryRow(ctx, "select id from graph_uploads where id=$1 and repository_id=$2 and schema_version=2 for update", uploadID, repositoryID).Scan(&id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "delete from graph_v2_discovery where upload_id=$1", id); err != nil {
		return err
	}
	if err = writeGraphDiscovery(ctx, tx, id, a); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func discoveryTSQuery(terms []string) string {
	out := []string{}
	for _, term := range terms {
		lexeme := graphquery.DiscoveryLexeme(term)
		// Terms are Unicode alphanumerics from the parser; quote as a second guard.
		q := "'" + strings.ReplaceAll(strings.ReplaceAll(lexeme, `\`, `\\`), "'", "''") + "'"
		if len(term) <= 1000 {
			q += ":*"
		}
		out = append(out, q)
	}
	return strings.Join(out, " | ")
}

func (s *Store) QueryDiscovery(ctx context.Context, q graphquery.DiscoverySearch) ([]graphprotocol.DiscoveryMatch, error) {
	ctx, cancel := s.graphQueryContext(ctx)
	defer cancel()
	if q.Limit <= 0 || q.Limit > 1001 || len(q.Terms) > 128 || len(q.Groups) > 128 {
		return nil, graphquery.ErrInvalidRequest
	}
	ids, uploads, commits := graphScope(q.Snapshots)
	var ready int
	if err := s.pool.QueryRow(ctx, `with scope as (select * from unnest($1::bigint[],$2::bigint[],$3::text[]) as v(repository_id,upload_id,commit)) select count(*) from scope join graph_uploads u on u.id=scope.upload_id and u.repository_id=scope.repository_id and u.commit=scope.commit and u.discovery_version=1`, ids, uploads, commits).Scan(&ready); err != nil {
		return nil, err
	}
	if ready != len(q.Snapshots) {
		return nil, graphquery.ErrDiscoveryUnavailable
	}
	groups := []string{}
	for _, g := range q.Groups {
		groups = append(groups, discoveryTSQuery(g))
	}
	raw := graphquery.NormalizeDiscovery(q.Query.Text)
	grams := graphquery.DiscoveryGrams(raw)
	patterns, negated := graphquery.DiscoveryPatterns(q.Deprioritize)
	distance := 0
	if q.Fuzzy {
		distance = 2
		if len([]rune(raw)) <= 4 {
			distance = 1
		}
	}
	args := []any{ids, uploads, commits, discoveryTSQuery(q.Terms), groups, q.Symbols, q.Files, q.Query.Kinds, q.Query.Languages, q.Query.Paths, q.Query.Names, raw, grams, q.Limit, graphquery.MaxEntityQueryBytes, patterns, distance, negated, q.ExplicitSymbols, q.ExplicitFiles, q.NoMultiterm}
	// Each predicate remains inside the trusted generation scope. Materializing
	// candidate IDs before payload joins bounds facts decoded and transferred.
	sql := `with scope as (select * from unnest($1::bigint[],$2::bigint[],$3::text[]) as v(repository_id,upload_id,commit)),
 search as (select to_tsquery('simple',$4) query),
 ranked as materialized (
 select u.repository_id,u.id upload_id,d.occurrence_key,d.occurrence_key occurrence,d.path,
 d.usage_count,d.generated,d.ambient,d.test_file,
 case when $17>0 then graph_discovery_distance(d.name,$12,$17) else 0 end edit_distance,
 graph_discovery_deprioritized(d.path,$16::text[],$18::boolean[]) deprioritized,
 (not $21::boolean and not d.generated and not d.ambient and not d.test_file and (d.usage_count>0 or d.kind in ('function','method','class','struct','interface','trait','protocol','component','route','enum','type_alias','union')) and not graph_discovery_deprioritized(d.path,$16::text[],$18::boolean[]) and (select count(*) from unnest($5::text[]) g where d.terms @@ to_tsquery('simple',g))>=2) corroborated,
 coalesce(((d.name=any($19::text[]) or d.path=any($20::text[])) or ((d.name=any($6::text[]) or d.path=any($7::text[])) and not graph_discovery_deprioritized(d.path,$16::text[],$18::boolean[]))),false) pinned,
 (select count(*) from unnest($5::text[]) g where d.terms @@ to_tsquery('simple',g)) matched,
 array_remove(array[
 case when $17>0 or d.name_terms @@ search.query or ($12<>'' and position($12 in d.name)>0) then 'name' end,
 case when d.qualified_terms @@ search.query or ($12<>'' and position($12 in d.qualified_name)>0) then 'qualified_name' end,
 case when d.signature_terms @@ search.query or ($12<>'' and position($12 in d.signature)>0) then 'signature' end,
 case when d.documentation_terms @@ search.query or ($12<>'' and position($12 in d.documentation)>0) then 'documentation' end,
 case when d.path_terms @@ search.query or ($12<>'' and position($12 in d.path)>0) then 'path' end,
 case when d.kind=any($8::text[]) or to_tsvector('simple',d.kind) @@ search.query then 'kind' end,
 case when d.language=any($9::text[]) or to_tsvector('simple',d.language) @@ search.query then 'language' end],null) fields,
 (1+ts_rank(array[0.05,0.1,0.25,1.0]::real[],d.terms,search.query)*100+least(d.usage_count,20)*0.2)
 *case when d.name=any($6::text[]) or d.path=any($7::text[]) then 1 else
 least(case when d.generated then 0.3 else 1 end,case when d.ambient then 0.5 else 1 end)*case when d.test_file then 0.5 else 1 end end
 *case when graph_discovery_deprioritized(d.path,$16::text[],$18::boolean[]) then 0.25 else 1 end
 /(1+case when $17>0 then graph_discovery_distance(d.name,$12,$17) else 0 end)
 *case when d.kind in ('variable','constant','parameter','property','field','enum_member') and d.usage_count=0 then 0.08 when d.kind in ('file','import','export') then 0.5 else 1 end score
 from scope join graph_uploads u on u.id=scope.upload_id and u.repository_id=scope.repository_id and u.commit=scope.commit and u.schema_version=2
 join graph_v2_discovery d on d.upload_id=u.id
 cross join search
 where (coalesce(cardinality($8::text[]),0)=0 or d.kind=any($8::text[]))
 and (coalesce(cardinality($9::text[]),0)=0 or d.language=any($9::text[]))
 and (coalesce(cardinality($10::text[]),0)=0 or exists(select from unnest($10::text[]) p where position(p in d.path)>0))
 and (coalesce(cardinality($11::text[]),0)=0 or exists(select from unnest($11::text[]) p where position(p in d.name)>0))
 and (($17>0 and char_length(d.name) between char_length($12)-$17 and char_length($12)+$17 and graph_discovery_distance(d.name,$12,$17)<=$17) or ($17=0 and (($12='' and coalesce(cardinality($6::text[]),0)=0 and coalesce(cardinality($7::text[]),0)=0 and (coalesce(cardinality($8::text[]),0)+coalesce(cardinality($9::text[]),0)+coalesce(cardinality($10::text[]),0)+coalesce(cardinality($11::text[]),0)>0))
 or d.terms @@ search.query
 or (md5(d.name)=any(array(select md5(v) from unnest($6::text[]) v)) and d.name=any($6::text[]))
 or (md5(d.path)=any(array(select md5(v) from unnest($7::text[]) v)) and d.path=any($7::text[]))
 or (cardinality($13::text[])>0 and d.grams @> $13::text[] and (position($12 in d.name)>0 or position($12 in d.qualified_name)>0 or position($12 in d.signature)>0 or position($12 in d.documentation)>0 or position($12 in d.path)>0)))))
 order by pinned desc,edit_distance,corroborated desc,score desc,u.repository_id,d.occurrence_key limit $14
 )
 select r.repository_id,u.public_repository,u.producer_name,u.producer_version,u.producer_configuration,
 case when octet_length(n.payload)<=$15 then n.payload end,
 case when octet_length(f.payload)<=$15 then f.payload end,f.payload is not null,
 r.score,r.fields,r.matched,r.usage_count,r.pinned,r.generated,r.ambient,r.test_file,r.deprioritized,r.edit_distance
 from ranked r join graph_uploads u on u.id=r.upload_id
 join graph_v2_nodes n on n.upload_id=r.upload_id and n.occurrence_key=r.occurrence_key
 left join graph_v2_files f on f.upload_id=n.upload_id and f.path_key=sha256(n.path) and f.path=n.path
 order by r.pinned desc,r.edit_distance,r.corroborated desc,r.score desc,r.repository_id,r.occurrence`
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []graphprotocol.DiscoveryMatch{}
	used := 0
	for rows.Next() {
		var m graphprotocol.DiscoveryMatch
		var repository string
		var name, version, configuration, payload, file []byte
		var hasFile bool
		if err = rows.Scan(&m.Entity.RepositoryID, &repository, &name, &version, &configuration, &payload, &file, &hasFile, &m.Score, &m.Fields, &m.MatchedTerms, &m.UsageCount, &m.Pinned, &m.Generated, &m.Ambient, &m.Test, &m.Deprioritized, &m.EditDistance); err != nil {
			return nil, err
		}
		if payload == nil || (hasFile && file == nil) {
			return nil, graphquery.ErrQuerySize
		}
		m.Fuzzy = q.Fuzzy
		m.Entity.Fact = new(graphv2.Node)
		if err = proto.Unmarshal(payload, m.Entity.Fact); err != nil {
			return nil, err
		}
		if hasFile {
			m.File = new(graphv2.File)
			if err = proto.Unmarshal(file, m.File); err != nil {
				return nil, err
			}
		}
		m.Entity.ID, err = graphartifact.IdentityV2(&graphv2.Producer{Name: string(name), Version: string(version), Configuration: string(configuration)}, repository, m.Entity.Fact.SourceId, m.Entity.Fact.Occurrence)
		if err != nil {
			return nil, err
		}
		if err = graphquery.AddEntityQueryBytes(&used, m); err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if len(result) == 0 && !q.Fuzzy && len([]rune(raw)) >= 3 {
		q.Fuzzy = true
		return s.QueryDiscovery(ctx, q)
	}
	return result, nil
}
