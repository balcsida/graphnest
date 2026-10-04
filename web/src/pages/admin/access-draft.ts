export interface AccessDraft {
  id: string
  administrator: boolean
  repositories: string
}

export const emptyDraft: AccessDraft = { id: '', administrator: false, repositories: '' }
