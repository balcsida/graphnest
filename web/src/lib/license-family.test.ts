import { describe, expect, it } from 'vitest'
import { licenseFamily, type LicenseFamily } from './license-family'

describe('licenseFamily', () => {
  const table: [string | null | undefined, LicenseFamily][] = [
    ['MIT', 'permissive'],
    ['mit', 'permissive'],
    ['Apache-2.0', 'permissive'],
    ['MPL-2.0', 'weak_copyleft'],
    ['LGPL-2.1-or-later', 'weak_copyleft'],
    ['GPL-3.0-only', 'strong_copyleft'],
    ['GPL-2.0+', 'strong_copyleft'],
    ['AGPL-3.0-or-later', 'strong_copyleft'],
    ['Some-Unlisted-1.0', 'other'],
    ['LicenseRef-custom', 'other'],
    ['NOASSERTION', 'unknown'],
    ['NONE', 'unknown'],
    ['', 'unknown'],
    ['   ', 'unknown'],
    [undefined, 'unknown'],
    [null, 'unknown'],
    ['MIT AND Apache-2.0', 'permissive'],
    ['MIT OR GPL-3.0-only', 'strong_copyleft'],
    ['(MIT OR Apache-2.0) AND LGPL-3.0-only', 'weak_copyleft'],
    ['MIT AND Some-Unlisted-1.0', 'other'],
    ['MIT AND NOASSERTION', 'unknown'],
    ['Some-Unlisted-1.0 AND NOASSERTION', 'other'],
    ['GPL-2.0-only WITH Classpath-exception-2.0', 'strong_copyleft'],
    ['Apache-2.0 WITH LLVM-exception', 'permissive'],
    ['mit and apache-2.0', 'permissive'],
  ]
  it.each(table)('classifies %j as %s', (expression, family) => {
    expect(licenseFamily(expression)).toBe(family)
  })
})
