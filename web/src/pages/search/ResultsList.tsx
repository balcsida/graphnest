import type { SearchMatch } from '@/api/types'
import { Button } from '@/components/ui/button'
import { blobUrl, countLabel, matchTarget, previewLines, shortSha, type FileTarget, type RepositoryGroup } from './model'

function MatchBlock({ match }: { match: SearchMatch }) {
  return (
    <div className="border-t py-1">
      <div className="overflow-x-auto">
        {previewLines(match.preview).map((text, offset) => {
          const line = match.line_number ? match.line_number + offset : 0
          return (
            <div key={offset} className={`flex min-w-max ${line === match.line_number ? 'bg-accent/60' : ''}`}>
              <span className="w-14 shrink-0 pr-3 text-right font-mono text-xs leading-6 text-muted-foreground select-none">{line || ''}</span>
              <code className="font-mono text-[13px] leading-6 whitespace-pre">{text}</code>
            </div>
          )
        })}
      </div>
    </div>
  )
}

/** Matches grouped by repository then file. Paths are buttons only when the server allows file reads. */
export function ResultsList({
  groups,
  fileReads,
  onOpen,
}: {
  groups: RepositoryGroup[]
  fileReads: boolean
  onOpen: (target: FileTarget, trigger: HTMLElement) => void
}) {
  return (
    <div className="grid gap-6">
      {groups.map((group) => (
        <section key={group.name} className="grid gap-3">
          <h2 className="text-base font-semibold break-all">{`${group.name} · ${countLabel(group.matchCount, 'match')} · ${shortSha(group.sha)}`}</h2>
          {group.files.map((file) => {
            const first = file.matches[0]
            const href = blobUrl({ web_url: first.repository.web_url, sha: first.sha, path: first.path, line: first.line_number })
            return (
              <article key={file.path} className="rounded-md border">
                <header className="flex flex-wrap items-center justify-between gap-2 px-3 py-2">
                  <h3 className="min-w-0 font-mono text-sm break-all">
                    {fileReads ? (
                      <Button type="button" variant="link" className="h-auto p-0 font-mono" onClick={(event) => onOpen(matchTarget(first), event.currentTarget)}>
                        {file.path}
                      </Button>
                    ) : (
                      <span>{file.path}</span>
                    )}
                  </h3>
                  {href && (
                    <a href={href} target="_blank" rel="noopener noreferrer" className="text-sm underline underline-offset-4">
                      Open indexed source
                    </a>
                  )}
                </header>
                {file.matches.map((match, index) => (
                  <MatchBlock key={`${match.line_number}-${index}`} match={match} />
                ))}
              </article>
            )
          })}
        </section>
      ))}
    </div>
  )
}
