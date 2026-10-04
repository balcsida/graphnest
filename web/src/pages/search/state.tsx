import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode, type RefObject } from 'react'
import { useNavigate } from 'react-router'
import { useAuth } from '@/lib/auth'
import type { FileTarget } from './model'

/** A submitted search. `repositories` is omitted for "all authorized" and may be empty (a scope error). */
export interface Submission {
  id: number
  query: string
  repositories?: string[]
}

interface SearchState {
  query: string
  setQuery: (query: string) => void
  language: string
  setLanguage: (language: string) => void
  allRepositories: boolean
  setAllRepositories: (all: boolean) => void
  selected: ReadonlySet<string>
  toggleRepository: (name: string, checked: boolean) => void
  submission: Submission | null
  submit: (query: string, repositories: string[] | undefined) => void
  file: FileTarget | null
  openFile: (target: FileTarget, trigger?: HTMLElement | null) => void
  /** Closes the file viewer without moving focus (the search page unmounted). */
  dropFile: () => void
  /** Closes the file viewer and asks the search page to restore focus to the opener. */
  closeFile: () => void
  /** Last element that opened a file, for focus restoration. */
  fileTrigger: RefObject<HTMLElement | null>
  queryInput: RefObject<HTMLInputElement | null>
  /** Bumped by the "/" shortcut; the search page focuses the query input when it changes. */
  focusTick: number
  /** Bumped by closeFile; the search page then restores focus to the opener. */
  restoreTick: number
}

const SearchContext = createContext<SearchState | null>(null)

const isEditable = (target: EventTarget | null) => target instanceof Element && target.closest('input,textarea,select,[contenteditable]') !== null

/**
 * Principal-scoped search state that survives moving between the Search and Repositories pages:
 * query, scope, last submission and open file. Registered with the auth provider so sign-out clears it.
 */
export function SearchStateProvider({ children }: { children: ReactNode }) {
  const { registerPrincipalReset } = useAuth()
  const navigate = useNavigate()
  const [query, setQuery] = useState('')
  const [language, setLanguage] = useState('')
  const [allRepositories, setAllRepositories] = useState(true)
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set())
  const [submission, setSubmission] = useState<Submission | null>(null)
  const [file, setFile] = useState<FileTarget | null>(null)
  const [focusTick, setFocusTick] = useState(0)
  const fileTrigger = useRef<HTMLElement | null>(null)
  const [restoreTick, setRestoreTick] = useState(0)
  const queryInput = useRef<HTMLInputElement | null>(null)
  const nextId = useRef(0)

  useEffect(
    () =>
      registerPrincipalReset(() => {
        setQuery('')
        setLanguage('')
        setAllRepositories(true)
        setSelected(new Set())
        setSubmission(null)
        setFile(null)
        fileTrigger.current = null
      }),
    [registerPrincipalReset],
  )

  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if (event.key !== '/' || event.ctrlKey || event.metaKey || event.altKey || isEditable(event.target)) return
      event.preventDefault()
      setFile(null)
      void navigate('/')
      setFocusTick((tick) => tick + 1)
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [navigate])

  const toggleRepository = useCallback((name: string, checked: boolean) => {
    setSelected((current) => {
      const next = new Set(current)
      if (checked) next.add(name)
      else next.delete(name)
      return next
    })
  }, [])

  const submit = useCallback((text: string, repositories: string[] | undefined) => {
    setSubmission({ id: ++nextId.current, query: text, repositories })
  }, [])

  const openFile = useCallback((target: FileTarget, trigger?: HTMLElement | null) => {
    if (trigger !== undefined) fileTrigger.current = trigger
    setFile(target)
  }, [])

  const dropFile = useCallback(() => {
    fileTrigger.current = null
    setFile(null)
  }, [])

  const closeFile = useCallback(() => {
    setRestoreTick((tick) => tick + 1)
    setFile(null)
  }, [])

  const value = useMemo<SearchState>(
    () => ({
      query,
      setQuery,
      language,
      setLanguage,
      allRepositories,
      setAllRepositories,
      selected,
      toggleRepository,
      submission,
      submit,
      file,
      openFile,
      dropFile,
      closeFile,
      fileTrigger,
      queryInput,
      focusTick,
      restoreTick,
    }),
    [query, language, allRepositories, selected, toggleRepository, submission, submit, file, openFile, dropFile, closeFile, focusTick, restoreTick],
  )
  return <SearchContext value={value}>{children}</SearchContext>
}

// eslint-disable-next-line react-refresh/only-export-components
export function useSearchState(): SearchState {
  const value = useContext(SearchContext)
  if (!value) throw new Error('useSearchState must be used inside SearchStateProvider')
  return value
}
