import { useSearchState } from './state'
import { useRepositories } from './use-repositories'

/** Repository names to send as the search scope, in list order; undefined means every authorized repository. */
export function useScope(): string[] | undefined {
  const { allRepositories, selected } = useSearchState()
  const { repositories } = useRepositories()
  return allRepositories ? undefined : repositories.filter((repository) => selected.has(repository.name)).map((repository) => repository.name)
}
