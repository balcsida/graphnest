-- Rebuildable semantic projection. Every original fact remains in v2 bytea.
alter table graph_uploads add column discovery_version integer not null default 0;
create table graph_v2_discovery (
 upload_id bigint not null,
 occurrence_key bytea not null,
 name text not null,
 qualified_name text not null,
 signature text not null,
 documentation text not null,
 path text not null,
 language text not null,
 kind text not null,
 name_document text not null,
 name_terms tsvector generated always as (to_tsvector('simple'::regconfig,name_document)) stored,
 qualified_document text not null,
 qualified_terms tsvector generated always as (to_tsvector('simple'::regconfig,qualified_document)) stored,
 signature_document text not null,
 signature_terms tsvector generated always as (to_tsvector('simple'::regconfig,signature_document)) stored,
 documentation_document text not null,
 documentation_terms tsvector generated always as (to_tsvector('simple'::regconfig,documentation_document)) stored,
 path_document text not null,
 path_terms tsvector generated always as (to_tsvector('simple'::regconfig,path_document)) stored,
 terms tsvector generated always as (setweight(to_tsvector('simple'::regconfig,name_document),'A') || setweight(to_tsvector('simple'::regconfig,qualified_document),'B') || setweight(to_tsvector('simple'::regconfig,signature_document),'C') || setweight(to_tsvector('simple'::regconfig,documentation_document),'D') || setweight(to_tsvector('simple'::regconfig,path_document),'C') || to_tsvector('simple'::regconfig,language) || to_tsvector('simple'::regconfig,kind)) stored,
 grams text[] not null,
 usage_count integer not null,
 generated boolean not null,
 ambient boolean not null,
 test_file boolean not null,
 primary key(upload_id,occurrence_key),
 foreign key(upload_id,occurrence_key) references graph_v2_nodes(upload_id,occurrence_key) on delete cascade
);
create index graph_v2_discovery_terms on graph_v2_discovery using gin(terms);
create index graph_v2_discovery_grams on graph_v2_discovery using gin(grams);
create index graph_v2_discovery_name on graph_v2_discovery(upload_id,md5(name));
create index graph_v2_discovery_path on graph_v2_discovery(upload_id,md5(path));

-- Bounded Levenshtein fallback uses rolling bands of at most seven cells.
-- It runs only after indexed semantic and substring search returned no rows.
create index graph_v2_discovery_name_length on graph_v2_discovery(upload_id,char_length(name));
create function graph_discovery_distance(a text,b text,maximum integer)
returns integer language plpgsql immutable strict as $$
declare
 aa text[] := string_to_array(a,null); bb text[] := string_to_array(b,null);
 al integer := char_length(a); bl integer := char_length(b);
 width integer := 2*maximum+3;
 previous integer[]; current_row integer[];
 i integer; j integer; row_min integer; value integer;
begin
 if a=b then return 0; end if;
 if abs(al-bl)>maximum then return maximum+1; end if;
 if al=0 then return bl; end if;
 if bl=0 then return al; end if;
 previous:=array_fill(maximum+1,array[width]);
 for j in 0..least(bl,maximum) loop previous[j%width+1]:=j; end loop;
 for i in 1..al loop
  current_row:=array_fill(maximum+1,array[width]);
  if i<=maximum then current_row[1]:=i; end if;
  row_min:=maximum+1;
  for j in greatest(1,i-maximum)..least(bl,i+maximum) loop
   value:=least(previous[j%width+1]+1,current_row[(j-1)%width+1]+1,
    previous[(j-1)%width+1]+case when aa[i]=bb[j] then 0 else 1 end);
   current_row[j%width+1]:=value;row_min:=least(row_min,value);
  end loop;
  if row_min>maximum then return maximum+1; end if;
  previous:=current_row;
 end loop;
 return previous[bl%width+1];
end $$;

-- Apply gitignore-style rules to each ancestor before its children: a negated
-- child cannot re-include itself beneath a still-ignored directory.
create function graph_discovery_deprioritized(path text,patterns text[],negated boolean[])
returns boolean language plpgsql immutable as $$
declare
 parts text[]:=string_to_array(path,'/'); prefix text:='';
 component integer; rule integer; ignored boolean;
begin
 if coalesce(cardinality(patterns),0)=0 or path='' or starts_with(path,'..') then return false; end if;
 for component in 1..cardinality(parts) loop
  prefix:=prefix||parts[component];
  if component<cardinality(parts) then prefix:=prefix||'/'; end if;
  ignored:=false;
  for rule in 1..cardinality(patterns) loop
   if prefix ~* patterns[rule] then ignored:=not negated[rule]; end if;
  end loop;
  if ignored then return true; end if;
 end loop;
 return false;
end $$;
