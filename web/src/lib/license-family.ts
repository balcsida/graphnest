export const LICENSE_FAMILIES = ['permissive', 'weak_copyleft', 'strong_copyleft', 'other', 'unknown'] as const
export type LicenseFamily = (typeof LICENSE_FAMILIES)[number]

export const FAMILY_LABEL: Record<LicenseFamily, string> = {
  permissive: 'Permissive',
  weak_copyleft: 'Weak copyleft',
  strong_copyleft: 'Strong copyleft',
  other: 'Other',
  unknown: 'Unknown',
}

const listed = (family: LicenseFamily, ids: string[]): [string, LicenseFamily][] => ids.map((id) => [id.toLowerCase(), family])

/** Well-known SPDX identifiers, lower-cased. Anything not listed is "other". */
const FAMILY_BY_ID = new Map<string, LicenseFamily>([
  ...listed('permissive', [
    'MIT', 'MIT-0', 'Apache-2.0', 'Apache-1.1', 'BSD-2-Clause', 'BSD-3-Clause', 'BSD-3-Clause-Clear', '0BSD', 'ISC', 'Zlib', 'Unlicense', 'CC0-1.0',
    'BSL-1.0', 'PostgreSQL', 'Python-2.0', 'PSF-2.0', 'X11', 'WTFPL', 'Unicode-DFS-2016', 'Unicode-3.0', 'BlueOak-1.0.0', 'CC-BY-4.0', 'NCSA', 'curl',
    'AFL-3.0', 'Artistic-2.0', 'OpenSSL',
  ]),
  ...listed('weak_copyleft', [
    'LGPL-2.0', 'LGPL-2.0-only', 'LGPL-2.0-or-later', 'LGPL-2.1', 'LGPL-2.1-only', 'LGPL-2.1-or-later', 'LGPL-3.0', 'LGPL-3.0-only', 'LGPL-3.0-or-later',
    'MPL-1.1', 'MPL-2.0', 'EPL-1.0', 'EPL-2.0', 'CDDL-1.0', 'CDDL-1.1', 'CPL-1.0', 'MS-RL',
  ]),
  ...listed('strong_copyleft', [
    'GPL-1.0', 'GPL-1.0-only', 'GPL-1.0-or-later', 'GPL-2.0', 'GPL-2.0-only', 'GPL-2.0-or-later', 'GPL-3.0', 'GPL-3.0-only', 'GPL-3.0-or-later',
    'AGPL-1.0', 'AGPL-1.0-only', 'AGPL-1.0-or-later', 'AGPL-3.0', 'AGPL-3.0-only', 'AGPL-3.0-or-later', 'SSPL-1.0',
  ]),
])

/** Strictest last. "unknown" outranks permissive: a permissive operand cannot vouch for an unasserted one. */
const STRICTNESS: LicenseFamily[] = ['permissive', 'unknown', 'other', 'weak_copyleft', 'strong_copyleft']

const UNASSERTED = new Set(['', 'noassertion', 'none'])

const familyOfIdentifier = (identifier: string): LicenseFamily => {
  const id = identifier.toLowerCase()
  if (UNASSERTED.has(id)) return 'unknown'
  return FAMILY_BY_ID.get(id) ?? FAMILY_BY_ID.get(id.replace(/\+$/, '')) ?? 'other'
}

/**
 * Family of an SPDX identifier or expression. AND, OR and WITH classify by the strictest operand; the
 * exception after WITH only relaxes its license, so it is not an operand. NOASSERTION, NONE and the empty
 * string are "unknown"; an identifier outside the table is "other".
 */
export function licenseFamily(expression: string | null | undefined): LicenseFamily {
  const tokens = (expression ?? '').replace(/[()]/g, ' ').split(/\s+/).filter(Boolean)
  let strictest: LicenseFamily | undefined
  for (let index = 0; index < tokens.length; index++) {
    const token = tokens[index]
    if (/^(and|or)$/i.test(token)) continue
    if (/^with$/i.test(token)) {
      index++
      continue
    }
    const family = familyOfIdentifier(token)
    if (!strictest || STRICTNESS.indexOf(family) > STRICTNESS.indexOf(strictest)) strictest = family
  }
  return strictest ?? 'unknown'
}
