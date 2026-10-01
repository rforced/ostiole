import { describe, expect, it } from 'vitest'
import { ref } from 'vue'

import { addressKey, byAddress, byNumber, byText, byTime, useSort } from '@/lib/sort'

describe('addressKey', () => {
  const order = (list) => [...list].sort((a, b) => (addressKey(a) < addressKey(b) ? -1 : 1))

  it('orders addresses as numbers, IPv4 first', () => {
    expect(order(['10.0.0.100', '10.0.0.9', '2001:db8::1', '9.9.9.9', '10.0.0.20'])).toEqual([
      '9.9.9.9',
      '10.0.0.9',
      '10.0.0.20',
      '10.0.0.100',
      '2001:db8::1',
    ])
  })

  it('reads every way of writing an IPv6 address', () => {
    expect(addressKey('2001:db8::1')).toBe(addressKey('2001:0db8:0:0:0:0:0:0001'))
    expect(addressKey('::')).toBe(`6${'0'.repeat(32)}`)
    expect(addressKey('fe80::1%eth0')).toBe(addressKey('fe80::1'))
    expect(addressKey('::ffff:192.0.2.1')).toBe(addressKey('::ffff:c000:201'))
    expect(order(['2001:db8::10', '2001:db8::9', '2001:db8:0:1::'])).toEqual([
      '2001:db8::9',
      '2001:db8::10',
      '2001:db8:0:1::',
    ])
  })

  it('puts what is not an address after the addresses', () => {
    expect(addressKey('')).toBe(null)
    expect(addressKey('printer') > addressKey('2001:db8::1')).toBe(true)
    expect(addressKey('1::2::3').startsWith('9')).toBe(true)
  })
})

describe('useSort', () => {
  const hosts = () =>
    ref([
      { ip: '10.0.0.20', name: 'printer10', seen: '2026-09-25T10:00:00Z', signal: -60 },
      { ip: '10.0.0.3', name: 'printer2', seen: '', signal: -40 },
      { ip: '10.0.0.100', name: '', seen: '2026-09-25T12:00:00Z', signal: null },
    ])
  const columns = {
    ip: byAddress((r) => r.ip),
    name: byText((r) => r.name),
    seen: byTime((r) => r.seen),
    signal: byNumber((r) => r.signal),
  }
  const ips = (s) => s.sorted.value.map((r) => r.ip)

  it('sorts a column the way it reads first, then reverses', () => {
    const s = useSort(hosts(), columns, { by: 'ip' })
    expect(ips(s)).toEqual(['10.0.0.3', '10.0.0.20', '10.0.0.100'])
    s.toggle('ip')
    expect(ips(s)).toEqual(['10.0.0.100', '10.0.0.20', '10.0.0.3'])
    // Names A to Z with numbers read as numbers; the empty one last.
    s.toggle('name')
    expect(s.order.value).toEqual({ by: 'name', dir: 'asc' })
    expect(ips(s)).toEqual(['10.0.0.3', '10.0.0.20', '10.0.0.100'])
    // Times and numbers start with the newest and the largest.
    s.toggle('seen')
    expect(s.order.value).toEqual({ by: 'seen', dir: 'desc' })
    expect(ips(s)).toEqual(['10.0.0.100', '10.0.0.20', '10.0.0.3'])
    s.toggle('signal')
    expect(ips(s)).toEqual(['10.0.0.3', '10.0.0.20', '10.0.0.100'])
  })

  it('keeps empty values last either way', () => {
    const s = useSort(hosts(), columns, { by: 'name' })
    expect(ips(s).at(-1)).toBe('10.0.0.100')
    s.toggle('name')
    expect(ips(s)).toEqual(['10.0.0.20', '10.0.0.3', '10.0.0.100'])
  })

  it('breaks ties by the tie column, then by the order rows came in', () => {
    const rows = ref([
      { ip: '10.0.0.9', name: 'b' },
      { ip: '10.0.0.2', name: 'a' },
      { ip: '10.0.0.1', name: 'b' },
    ])
    expect(ips(useSort(rows, columns, { by: 'name', tie: 'ip' }))).toEqual([
      '10.0.0.2',
      '10.0.0.1',
      '10.0.0.9',
    ])
    expect(ips(useSort(rows, columns, { by: 'name' }))).toEqual([
      '10.0.0.2',
      '10.0.0.9',
      '10.0.0.1',
    ])
  })

  // A cell's rate and total under one header: a click moves to the other,
  // each the way it reads, rather than reversing.
  it('moves through the columns of one header', () => {
    const rows = ref([
      { ip: '10.0.0.1', rate: 5, total: 100 },
      { ip: '10.0.0.2', rate: 9, total: 10 },
    ])
    const s = useSort(rows, {
      ip: byAddress((r) => r.ip),
      rate: byNumber((r) => r.rate),
      total: byNumber((r) => r.total),
    })
    const usage = ['rate', 'total']
    s.toggle(usage)
    expect(s.order.value).toEqual({ by: 'rate', dir: 'desc' })
    expect(ips(s)).toEqual(['10.0.0.2', '10.0.0.1'])
    s.toggle(usage)
    expect(s.order.value).toEqual({ by: 'total', dir: 'desc' })
    expect(ips(s)).toEqual(['10.0.0.1', '10.0.0.2'])
    s.toggle(usage)
    expect(s.order.value).toEqual({ by: 'rate', dir: 'desc' })
    // From the select's choice, the next click moves on from it.
    s.choose('total')
    s.toggle(usage)
    expect(s.order.value).toEqual({ by: 'rate', dir: 'desc' })
    // From another column, it starts again at the first.
    s.toggle('ip')
    s.toggle(usage)
    expect(s.order.value).toEqual({ by: 'rate', dir: 'desc' })
    // A list of one is a column of its own, and reverses.
    s.toggle(['ip'])
    s.toggle(['ip'])
    expect(s.order.value).toEqual({ by: 'ip', dir: 'desc' })
  })

  it('takes a column in its own order from the select', () => {
    const s = useSort(hosts(), columns, { by: 'ip', dir: 'desc' })
    s.choose('seen')
    expect(s.order.value).toEqual({ by: 'seen', dir: 'desc' })
    s.choose('ip')
    expect(s.order.value).toEqual({ by: 'ip', dir: 'asc' })
    s.choose('nothing')
    expect(s.order.value).toEqual({ by: 'ip', dir: 'asc' })
  })

  // A list with an order of its own keeps it until a header is clicked.
  it('keeps the order rows came in until told otherwise', () => {
    const s = useSort(hosts(), columns)
    expect(s.order.value.by).toBe(null)
    expect(ips(s)).toEqual(['10.0.0.20', '10.0.0.3', '10.0.0.100'])
    s.toggle('ip')
    expect(ips(s)).toEqual(['10.0.0.3', '10.0.0.20', '10.0.0.100'])
  })

  it('follows the rows it is given', () => {
    const rows = hosts()
    const s = useSort(rows, columns, { by: 'ip' })
    rows.value = [...rows.value, { ip: '10.0.0.1', name: 'new' }]
    expect(ips(s)[0]).toBe('10.0.0.1')
  })
})
