import { readdirSync, readFileSync } from 'node:fs'
import path from 'node:path'
import { expect, it } from 'vitest'

const src = path.resolve(import.meta.dirname)
const allowed = path.join(src, 'components/ui/chart.tsx')

it('uses no HTML sinks outside components/ui/chart.tsx', () => {
  const offenders = readdirSync(src, { recursive: true, withFileTypes: true })
    .filter((entry) => entry.isFile() && /\.(ts|tsx)$/.test(entry.name))
    .map((entry) => path.join(entry.parentPath, entry.name))
    .filter((file) => file !== allowed && file !== import.meta.filename)
    .filter((file) => /dangerouslySetInnerHTML|innerHTML|outerHTML|insertAdjacentHTML/.test(readFileSync(file, 'utf8')))
  expect(offenders).toEqual([])
})
