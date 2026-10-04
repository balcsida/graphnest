import { describe, expect, it } from 'vitest'
import { downloadSupplyChainFile } from './supply-chain'

describe('downloadSupplyChainFile', () => {
  it.each(['//evil.example/x', 'https://evil.example/x', '/other/path'])('refuses %s before any request', async (path) => {
    await expect(downloadSupplyChainFile(path, 'x.json')).rejects.toThrow('unexpected path')
  })
})
