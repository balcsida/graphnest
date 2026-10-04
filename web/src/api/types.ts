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
