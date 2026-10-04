import type {
  AdminAccessRequest,
  AdminDeliveryList,
  AdminGitHub,
  AdminGroupList,
  AdminJobList,
  AdminOverview,
  AdminRepositoryList,
  AdminSCIPDependencyList,
  AdminSCIPUploadList,
  AdminUserAccessRequest,
  AdminUserList,
  AuditEventList,
  SCIPDependencyRefreshResponse,
} from './types'
import { request } from '@/lib/api'

const withCursor = (path: string, cursor?: string) => (cursor ? `${path}?cursor=${encodeURIComponent(cursor)}` : path)

/** Succeeds only for administrators; 401, 403 and 404 all mean "not an administrator". */
export const getAdminOverview = (signal?: AbortSignal) => request<AdminOverview>('/v1/admin/overview', { signal })

export const getAdminRepositories = (cursor?: string, signal?: AbortSignal) =>
  request<AdminRepositoryList>(withCursor('/v1/admin/repositories', cursor), { signal })

export const getAdminJobs = (cursor?: string, signal?: AbortSignal) =>
  request<AdminJobList>(withCursor('/v1/admin/jobs', cursor), { signal })

export const getAdminUsers = (signal?: AbortSignal) => request<AdminUserList>('/v1/admin/users', { signal })
export const getAdminGroups = (signal?: AbortSignal) => request<AdminGroupList>('/v1/admin/groups', { signal })
export const getAuditEvents = (signal?: AbortSignal) => request<AuditEventList>('/v1/admin/audit-events', { signal })
export const getScipUploads = (signal?: AbortSignal) => request<AdminSCIPUploadList>('/v1/admin/scip/uploads', { signal })
export const getScipDependencies = (signal?: AbortSignal) =>
  request<AdminSCIPDependencyList>('/v1/admin/scip/dependencies', { signal })
export const getWebhookDeliveries = (signal?: AbortSignal) =>
  request<AdminDeliveryList>('/v1/admin/webhook-deliveries', { signal })
export const getAdminGitHub = (signal?: AbortSignal) => request<AdminGitHub>('/v1/admin/github', { signal })

export const reindexRepository = (githubId: number) =>
  request<void>(`/v1/admin/repositories/${encodeURIComponent(githubId)}/reindex`, { method: 'POST' })
export const reconcileGitHub = () => request<void>('/v1/admin/reconcile', { method: 'POST' })
export const retryJob = (id: number) => request<void>(`/v1/admin/jobs/${encodeURIComponent(id)}/retry`, { method: 'POST' })

export const suspendUser = (id: number) => request<void>(`/v1/admin/users/${encodeURIComponent(id)}/suspend`, { method: 'POST' })
export const restoreUser = (id: number) => request<void>(`/v1/admin/users/${encodeURIComponent(id)}/restore`, { method: 'POST' })
export const revokeUserCredentials = (id: number) =>
  request<void>(`/v1/admin/users/${encodeURIComponent(id)}/revoke-credentials`, { method: 'POST' })
export const replaceUserAccess = (id: number, body: AdminUserAccessRequest) =>
  request<void>(`/v1/admin/users/${encodeURIComponent(id)}/access`, { method: 'PUT', body })
export const replaceGroupAccess = (id: number, body: AdminAccessRequest) =>
  request<void>(`/v1/admin/groups/${encodeURIComponent(id)}/access`, { method: 'PUT', body })

export const uploadScipIndex = (repositoryId: number, commit: string, file: Blob) =>
  request<void>(`/v1/scip/uploads?repository_id=${encodeURIComponent(repositoryId)}&commit=${encodeURIComponent(commit)}`, {
    method: 'POST',
    body: file,
    contentType: 'application/vnd.scip+protobuf',
  })
export const refreshGitHubDependencies = (repositoryId: number) =>
  request<SCIPDependencyRefreshResponse>('/v1/scip/dependencies/github', { method: 'POST', body: { repository_id: repositoryId } })
