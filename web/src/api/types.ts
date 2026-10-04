// Mirrors the schemas in docs/openapi.yaml. Timestamps are RFC 3339 strings and
// int64 values are numbers. Optional properties are the ones missing from `required`.

export interface AuthConfig {
  token_login: boolean
  break_glass: boolean
  file_reads: boolean
  providers: AuthProvider[]
}

export interface AuthProvider {
  id: string
  label: string
  login_url: string
}

export interface AuthSession {
  method: 'bearer' | 'oidc' | 'oauth' | 'local'
}

export interface ErrorResponse {
  error: {
    code: string
    message: string
    request_id: string
    retryable: boolean
  }
}

export type ScipStatus = 'current' | 'stale' | 'absent' | 'unknown'

export interface RepositorySummary {
  id: number
  github_id: number
  name: string
  branch: string
  desired_sha: string
  indexed_sha: string
  web_url: string
  status: string
  error_code: string
  search_node: string
  last_indexed_at?: string
  scip_status: ScipStatus
  scip_commit?: string
}

export interface RepositoryList {
  repositories: RepositorySummary[]
  truncated: boolean
  next_cursor?: string
}

export interface Repository {
  id: number
  name: string
  branch: string
  indexed_sha: string
  web_url: string
}

export interface SearchRequest {
  query: string
  repositories?: string[]
  limit?: number
  context_lines?: number
  /** Go duration in nanoseconds. */
  timeout?: number
  max_response_bytes?: number
}

export interface SearchConsistency {
  backend: 'github'
  exact: boolean
  revision?: string
  partial: boolean
}

export interface SearchMatch {
  repository: Repository
  path: string
  sha: string
  line_number: number
  line_start: number
  line_end: number
  preview: string
  score: number
}

export interface SearchResponse {
  matches: SearchMatch[]
  truncated: boolean
  consistency?: SearchConsistency
}

export interface ReadFileRequest {
  repository_id: number
  path: string
  start_line?: number
  end_line?: number
}

export interface ReadFileResponse {
  repository_id: number
  path: string
  indexed_sha: string
  blob_sha: string
  content: string
  start_line: number
  end_line: number
  truncated: boolean
}

export type ScipOperation = 'definitions' | 'references' | 'implementations'

/** The three offsets are sent together; the server rejects a partial set. */
export interface ScipNavigationRequest {
  repository_id: number
  path: string
  commit?: string
  line: number
  character_utf8: number
  character_utf16: number
  character_utf32: number
  operation: ScipOperation
}

export interface ScipLocation {
  repository_id: number
  repository_name: string
  branch: string
  web_url: string
  commit: string
  path: string
  symbol: string
  start_line: number
  start_character: number
  end_line: number
  end_character: number
  position_encoding: 'UTF8CodeUnitOffsetFromLineStart' | 'UTF16CodeUnitOffsetFromLineStart' | 'UTF32CodeUnitOffsetFromLineStart'
  roles: number
  approximate: boolean
}

export interface ScipNavigationResponse {
  locations: ScipLocation[]
  truncated: boolean
}

export interface AdminOverview {
  repositories: Record<string, number>
  jobs: Record<string, number>
  deliveries: Record<string, number>
  scip_uploads: number
  dependencies: number
  installations: number
}

export interface SupplyChainFacet {
  value: string
  count: number
}

export interface SupplyChainFacets {
  stream: string
  ecosystems: SupplyChainFacet[]
  assessments: SupplyChainFacet[]
  licenses: SupplyChainFacet[]
}

export interface SupplyChainOverview {
  stream: string
  generated_at: string
  repositories: {
    authorized: number
    with_inventory: number
    never_collected: number
    stale: number
    failed_last_attempt: number
    opted_out: number
  }
  components: {
    occurrences: number
    unique_coordinates: number
    without_purl: number
    without_version: number
    unassessed: number
    assessments: Record<string, number>
  }
  warning_total: number
  oldest_collected_at: string | null
  newest_collected_at: string | null
  ecosystems: SupplyChainFacet[]
  denominators: string[]
}

export interface SupplyChainPortfolioComponent {
  /** Opaque coordinate key for the detail route. */
  key: string
  ecosystem: string
  namespace?: string
  name: string
  version: string
  purl?: string
  repository_count: number
  occurrence_count: number
  /** Single status when all occurrences agree, otherwise mixed or unassessed. */
  assessment: string
  assessment_statuses: string[]
  expression?: string
  /** Distinct verbatim producer declarations across occurrences. */
  declared_raw: string[]
  newest_collected_at: string
  oldest_collected_at: string
  repositories: { id: number; name?: string }[]
}

export interface SupplyChainPortfolioComponentList {
  stream: string
  repositories_in_scope: number
  components: SupplyChainPortfolioComponent[]
  truncated: boolean
  next_cursor?: string
}

export interface SupplyChainPortfolioOccurrence {
  repository_id: number
  repository: string
  snapshot_id: number
  collected_at: string
  element_id: string
  root: boolean
  declared_raw: string | null
  assessment?: string
  expression?: string
  detail_path: string
}

export interface SupplyChainPortfolioComponentDetail {
  key: string
  stream: string
  ecosystem: string
  namespace?: string
  name: string
  version: string
  occurrences: SupplyChainPortfolioOccurrence[]
  truncated: boolean
  notes: string[]
}

export type SupplyChainProducer = 'github' | 'import'
export type SupplyChainSubject = 'source' | 'artifact'
export type SupplyChainDocumentFormat = 'spdx-2.3-json' | 'cyclonedx-1.6-json'

export interface SupplyChainWarning {
  code: string
  element?: string
  detail?: string
}

export interface SupplyChainDocumentRef {
  snapshot_id: number
  sha256: string
  format: SupplyChainDocumentFormat
  bytes: number
  path: string
}

export interface SupplyChainStreamRef {
  key: string
  producer: SupplyChainProducer
  subject: SupplyChainSubject
  has_inventory: boolean
  last_outcome?: string
}

export interface SupplyChainSnapshot {
  id: number
  /** Internal repository row ID, not the GitHub ID. */
  repository_id: number
  stream: string
  producer: SupplyChainProducer
  subject: SupplyChainSubject
  /** GraphNest's fetch time. */
  collected_at: string
  /** The producer's own creationInfo.created, displayed as a claim. */
  created_at_claimed: string | null
  producer_tool: string
  document_name: string
  document_namespace: string
  spdx_version: string
  /** License of the document itself, not of its components. */
  data_license: string
  subject_revision?: string
  subject_assurance: 'unknown' | 'producer_asserted' | 'verified'
  root_element_ids: string[]
  parser_version: number
  component_count: number
  edge_count: number
  warning_count: number
  warnings: SupplyChainWarning[]
  published_at: string
  document_sha256: string
  document_format: SupplyChainDocumentFormat
  document_bytes: number
  /** Authenticated uploader of an imported document. */
  uploaded_by?: string
  upload_label?: string
}

export type SupplyChainCollectionOutcome =
  | 'published'
  | 'unchanged'
  | 'unavailable'
  | 'forbidden'
  | 'rate_limited'
  | 'not_found'
  | 'malformed'
  | 'too_large'
  | 'transient'
  | 'cancelled'
  | 'error'

export interface SupplyChainCollection {
  id: number
  job_id: number | null
  producer: SupplyChainProducer
  stream: string
  started_at: string
  finished_at: string
  outcome: SupplyChainCollectionOutcome
  http_status: number | null
  retry_after_seconds?: number
  snapshot_id: number | null
  error_code?: string
  /** Operator-safe explanation; never echoes GitHub response bodies. */
  message?: string
  /** Set when the snapshot published but the SCIP package-mapping projection failed. */
  projection_error?: string
}

export interface SupplyChainJob {
  id: number
  /** GitHub repository ID. */
  repository_id: number
  stream: string
  reason: 'scheduled' | 'manual' | 'webhook'
  state: 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled' | 'superseded'
  attempt: number
  max_attempts: number
  run_after: string
  error_code?: string
  created_at: string
  updated_at: string
  lease_expires_at?: string
}

export interface SupplyChainRepositoryStatus {
  repository_id: number
  repository: string
  stream: string
  producer: SupplyChainProducer
  subject: SupplyChainSubject
  /** never, current, stale (older than twice the interval) or failed (last attempt did not succeed). */
  collection: 'never' | 'current' | 'stale' | 'failed'
  /** Age of the latest successful observation. */
  freshness_seconds: number | null
  latest_snapshot: SupplyChainSnapshot | null
  last_collection: SupplyChainCollection | null
  active_job: SupplyChainJob | null
  enrichment: 'not_configured' | 'configured'
  enrichment_ecosystems: ('npm' | 'nuget' | 'maven')[]
  /** Latest snapshot's assessment counts by status. */
  license_summary: Record<string, number>
  opt_out: boolean
  notes: string[]
  documents: SupplyChainDocumentRef[]
  /** Every stream known for the repository (GitHub observation and imports). */
  streams: SupplyChainStreamRef[]
}

export interface SupplyChainLicenseAssessment {
  status: 'unknown' | 'declared' | 'resolved' | 'conflict' | 'unlicensed' | 'not_applicable' | 'pending'
  /** Normalized SPDX expression when status is declared or resolved. */
  expression?: string
  conflict_detail?: string
  evidence_count: number
  assessed_at: string
  evidence_fingerprint: string
}

export interface SupplyChainComponent {
  /** Document-scoped identifier (SPDXID or bom-ref). */
  element_id: string
  ordinal: number
  name: string
  version: string | null
  purl: string | null
  /** Lowercase purl type. */
  ecosystem?: string
  namespace?: string
  package_name?: string
  qualifiers?: Record<string, string>
  /** Verbatim producer value; NOASSERTION and NONE are preserved, never mapped. */
  license_declared_raw: string | null
  license_concluded_raw: string | null
  download_location?: string
  supplier?: string
  checksums?: { algorithm: string; value: string }[]
  is_root: boolean
  scope: 'root' | 'direct' | 'transitive' | 'unknown'
  license?: SupplyChainLicenseAssessment | null
}

export interface SupplyChainComponentList {
  snapshot_id: number
  components: SupplyChainComponent[]
  truncated: boolean
  next_cursor?: string
}

export type SupplyChainEvidenceSource =
  | 'producer_declared'
  | 'producer_concluded'
  | 'registry_npm'
  | 'registry_nuget'
  | 'registry_maven'
  | 'import'
  | 'human'

export interface SupplyChainLicenseEvidence {
  /** 0 for producer declarations synthesized from the snapshot. */
  id: number
  source: SupplyChainEvidenceSource
  route?: string
  ecosystem: string
  namespace?: string
  name: string
  version: string
  artifact_sha256?: string
  /** Verbatim value as observed. */
  raw_value: string
  raw_kind:
    | 'expression'
    | 'expression_or_file'
    | 'license_file'
    | 'license_url'
    | 'license_name'
    | 'legacy_object'
    | 'missing'
    | 'sentinel'
  parse_status: 'parsed' | 'unknown_terms' | 'no_assertion' | 'none' | 'unlicensed' | 'invalid' | 'not_applicable'
  expression?: string
  unknown_terms?: string[]
  license_url?: string
  license_file_name?: string
  detail?: Record<string, unknown>
  resolver_version: number
  license_list_version: string
  content_sha256?: string
  fetched_at: string
  /** Present on negative outcomes; the lookup is retried after this time. */
  expires_at?: string
  outcome: 'resolved' | 'not_found' | 'no_license_metadata' | 'unavailable' | 'rejected' | 'too_large' | 'malformed'
  http_status?: number
  message?: string
}

export interface SupplyChainRelationship {
  from: string
  type: string
  to: string
  resolved: boolean
}

export interface SupplyChainComponentDetail {
  component: SupplyChainComponent
  snapshot: SupplyChainSnapshot
  declarations: SupplyChainLicenseEvidence[]
  /** At most 100 rows. */
  evidence: SupplyChainLicenseEvidence[]
  /** At most 100 rows. */
  relationships: SupplyChainRelationship[]
  notes: string[]
  truncated: boolean
}

export interface SupplyChainCollectionList {
  collections: SupplyChainCollection[]
  truncated: boolean
  next_cursor?: string
}

export interface SupplyChainSnapshotList {
  /** Newest first. */
  snapshots: SupplyChainSnapshot[]
}

export interface SupplyChainSnapshotComparison {
  repository_id: number
  base: SupplyChainSnapshot
  head: SupplyChainSnapshot
  added_components: string[]
  removed_components: string[]
  license_changes: { component: string; from: string; to: string }[]
  edges_added: number
  edges_removed: number
  metadata_changes: string[]
  notes: string[]
}

export interface SupplyChainRefreshResponse {
  job: SupplyChainJob
  created: boolean
}

export interface AdminRepository {
  id: number
  github_id: number
  installation_id: number
  name: string
  default_branch: string
  desired_sha: string
  indexed_sha: string
  status: string
  error_code: string
  web_url: string
  enabled: boolean
  private: boolean
  archived: boolean
  last_indexed_at?: string
}

export interface AdminRepositoryList {
  repositories: AdminRepository[]
  truncated: boolean
  next_cursor?: string
}

export interface AdminJob {
  id: number
  repository_id: number
  repository: string
  target_sha: string
  target_ref: string
  reason: string
  state: string
  error_code: string
  attempt: number
  max_attempts: number
  priority: number
  run_after: string
  created_at: string
  updated_at: string
}

export interface AdminJobList {
  jobs: AdminJob[]
  truncated: boolean
  next_cursor?: string
}

export interface AdminUser {
  id: number
  external_id: string
  user_name: string
  display_name: string
  source: 'scim' | 'local' | 'github'
  scim_active: boolean
  suspended: boolean
  administrator: boolean
  repository_ids: number[]
  direct_administrator: boolean
  direct_repository_ids: number[]
  github_repository_ids: number[]
}

export interface AdminUserList {
  users: AdminUser[]
  truncated: boolean
}

export interface AdminGroup {
  id: number
  external_id: string
  display_name: string
  administrator: boolean
  repository_ids: number[]
  member_count: number
}

export interface AdminGroupList {
  groups: AdminGroup[]
  truncated: boolean
}

export interface AdminAccessRequest {
  administrator: boolean
  repository_ids: number[]
}

export interface AdminUserAccessRequest {
  direct_administrator: boolean
  direct_repository_ids: number[]
}

export interface AuditEvent {
  actor_type: 'anonymous' | 'operator' | 'scim' | 'system' | 'user'
  actor_id: string
  target_type: 'api_token' | 'authentication' | 'group' | 'oauth_client' | 'oauth_grant' | 'session' | 'user'
  target_id: string
  authentication_method: '' | 'api_token' | 'local' | 'oauth' | 'oauth_token' | 'oidc' | 'operator' | 'scim_token'
  /** One of the audit operation names in docs/openapi.yaml; shown verbatim. */
  operation: string
  outcome: 'success' | 'denied' | 'invalid' | 'error'
  request_id: string
  created_at: string
}

export interface AuditEventList {
  events: AuditEvent[]
  truncated: boolean
}

export interface AdminSCIPUpload {
  id: number
  repository_id: number
  repository: string
  commit: string
  project_root: string
  indexer_name: string
  indexer_version: string
  uploaded_at: string
}

export interface AdminSCIPUploadList {
  uploads: AdminSCIPUpload[]
  truncated: boolean
}

export interface AdminSCIPDependency {
  repository_id: number
  repository: string
  source: 'manual' | 'github'
  relation: 'provides' | 'depends_on'
  purl: string
  manager: string
  name: string
  version: string
}

export interface AdminSCIPDependencyList {
  dependencies: AdminSCIPDependency[]
  truncated: boolean
}

export interface SCIPDependencyRefreshResponse {
  available: boolean
  packages: number
}

export interface AdminDelivery {
  id: number
  delivery_id: string
  event: string
  state: string
  error_code: string
  installation_id: number
  received_at: string
  processed_at?: string
}

export interface AdminDeliveryList {
  deliveries: AdminDelivery[]
  truncated: boolean
}

export interface AdminInstallation {
  github_id: number
  account_login: string
  account_type: string
  status: string
  suspended_at?: string
}

export interface AdminGitHub {
  app_id: number
  web_url: string
  api_url: string
  upload_url: string
  git_url: string
  api_version: string
  private_key_configured: boolean
  webhook_secret_configured: boolean
  ca_configured: boolean
  installations: AdminInstallation[]
  truncated: boolean
}

export interface APIToken {
  id: number
  prefix: string
  repository_ids?: number[]
  /** Present and true for delegation-only administrator tokens, which carry no repository ceiling. */
  delegation_only?: boolean
  created_at: string
  last_used_at?: string
  expires_at?: string
}

export interface APITokenList {
  tokens: APIToken[]
}

export interface CreateAPITokenRequest {
  expires_at?: string
  repository_ids?: number[]
}

export interface CreatedAPIToken {
  id: number
  prefix: string
  repository_ids?: number[]
  created_at: string
  expires_at?: string
  token: string
}

export interface CreateDelegationOnlyTokenRequest {
  expires_at?: string
}

export interface CreatedDelegationOnlyToken {
  id: number
  prefix: string
  delegation_only: true
  created_at: string
  expires_at?: string
  token: string
}

export interface OAuthGrant {
  id: number
  client_name: string
  scope?: string
  created_at: string
  last_used_at: string
  expires_at: string
}

export interface OAuthGrantList {
  grants: OAuthGrant[]
  truncated: boolean
  next_cursor?: string
}
