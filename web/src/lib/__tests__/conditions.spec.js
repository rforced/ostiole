import { describe, expect, it } from 'vitest'

import { conditionKey, formatCondition, parseCondition } from '@/lib/conditions'

describe('parseCondition', () => {
  it('reads a field and a value, or a bare key, as the router does', () => {
    expect(parseCondition('region=us-ashburn-1')).toEqual({
      field: 'region',
      value: 'us-ashburn-1',
    })
    expect(parseCondition(' tags = OCI ')).toEqual({ field: 'tags', value: 'OCI' })
    expect(parseCondition('service=Google Cloud')).toEqual({
      field: 'service',
      value: 'Google Cloud',
    })
    expect(parseCondition('note=a=b')).toEqual({ field: 'note', value: 'a=b' })
    expect(parseCondition('hooks')).toEqual({ field: '', value: 'hooks' })
    expect(parseCondition('region=us-*')).toEqual({ field: 'region', value: 'us-*' })
  })

  it('refuses half a condition, a pattern in a field name, and a long one', () => {
    for (const bad of ['', '  ', '=OCI', 'tags=', 're*gion=x', 'x'.repeat(129)]) {
      expect(parseCondition(bad), bad).toBeNull()
    }
  })
})

describe('formatCondition and conditionKey', () => {
  it('writes a field and a value, or the key alone', () => {
    expect(formatCondition('scope', 'us-east1')).toBe('scope=us-east1')
    expect(formatCondition('', 'hooks')).toBe('hooks')
  })

  it('treats two spellings of one condition as one', () => {
    expect(conditionKey(' Tags = oci ')).toBe(conditionKey('tags=OCI'))
    expect(conditionKey('HOOKS')).toBe('hooks')
    expect(conditionKey('tags=OCI')).not.toBe(conditionKey('tags=OSN'))
  })
})
