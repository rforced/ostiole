import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { defineComponent, ref } from 'vue'

import SortHeader from '@/components/SortHeader.vue'
import SortSelect from '@/components/SortSelect.vue'
import { byAddress, byNumber, byText, useSort } from '@/lib/sort'

/** A table of two columns. */
const Host = defineComponent({
  components: { SortHeader, SortSelect },
  setup() {
    const rows = ref([
      { ip: '10.0.0.20', name: 'b' },
      { ip: '10.0.0.3', name: 'a' },
    ])
    const sort = useSort(
      rows,
      { ip: byAddress((r) => r.ip), name: byText((r) => r.name) },
      { by: 'ip' },
    )
    return {
      sort,
      columns: [
        ['ip', 'Address'],
        ['name', 'Name'],
      ],
    }
  },
  template: `
    <SortSelect :sort="sort" :columns="columns" />
    <table>
      <thead><tr>
        <SortHeader by="ip" :sort="sort">Address</SortHeader>
        <SortHeader by="name" :sort="sort">Name</SortHeader>
      </tr></thead>
      <tbody><tr v-for="r in sort.sorted.value" :key="r.ip"><td>{{ r.ip }}</td></tr></tbody>
    </table>`,
})

/** A cell of two figures, a rate and a total, under one header. */
const Usage = {
  components: { SortHeader },
  setup() {
    const rows = ref([
      { ip: '10.0.0.1', rate: 5, total: 100 },
      { ip: '10.0.0.2', rate: 9, total: 10 },
    ])
    const sort = useSort(rows, {
      ip: byAddress((r) => r.ip),
      rate: byNumber((r) => r.rate),
      total: byNumber((r) => r.total),
    })
    return {
      sort,
      columns: [
        ['rate', 'Down'],
        ['total', 'Down total'],
      ],
    }
  },
  template: `
    <table>
      <thead><tr>
        <SortHeader by="ip" :sort="sort">Address</SortHeader>
        <SortHeader :columns="columns" :sort="sort" />
      </tr></thead>
      <tbody><tr v-for="r in sort.sorted.value" :key="r.ip"><td>{{ r.ip }}</td></tr></tbody>
    </table>`,
}

const ips = (w) => w.findAll('td').map((td) => td.text())
const header = (w, name) => w.findAll('th').find((th) => th.text() === name)
/** What a header over several columns reads: its one visible label. */
const reads = (th) =>
  th
    .findAll('button > span > span')
    .filter((s) => !s.classes('invisible'))
    .map((s) => s.text())

describe('SortHeader', () => {
  it('sorts by its column and says which way', async () => {
    const wrapper = mount(Host)
    expect(header(wrapper, 'Address').attributes('aria-sort')).toBe('ascending')
    expect(header(wrapper, 'Name').attributes('aria-sort')).toBeUndefined()
    expect(ips(wrapper)).toEqual(['10.0.0.3', '10.0.0.20'])

    await header(wrapper, 'Address').get('button').trigger('click')
    expect(header(wrapper, 'Address').attributes('aria-sort')).toBe('descending')
    expect(ips(wrapper)).toEqual(['10.0.0.20', '10.0.0.3'])

    await header(wrapper, 'Name').get('button').trigger('click')
    expect(header(wrapper, 'Address').attributes('aria-sort')).toBeUndefined()
    expect(header(wrapper, 'Name').attributes('aria-sort')).toBe('ascending')
  })

  // Each label stays in the header, the others hidden, so it keeps the
  // longest one's width.
  it('moves between the figures of a cell, and reads the one it sorts by', async () => {
    const wrapper = mount(Usage)
    const usage = wrapper.findAll('th')[1]
    expect(usage.findAll('button > span > span')).toHaveLength(2)
    expect(reads(usage)).toEqual(['Down'])
    expect(usage.attributes('aria-sort')).toBeUndefined()

    await usage.get('button').trigger('click')
    expect(reads(usage)).toEqual(['Down'])
    expect(usage.attributes('aria-sort')).toBe('descending')
    expect(ips(wrapper)).toEqual(['10.0.0.2', '10.0.0.1'])

    await usage.get('button').trigger('click')
    expect(reads(usage)).toEqual(['Down total'])
    expect(usage.attributes('aria-sort')).toBe('descending')
    expect(ips(wrapper)).toEqual(['10.0.0.1', '10.0.0.2'])

    await usage.get('button').trigger('click')
    expect(reads(usage)).toEqual(['Down'])

    await wrapper.findAll('th')[0].get('button').trigger('click')
    expect(usage.attributes('aria-sort')).toBeUndefined()
    expect(reads(usage)).toEqual(['Down'])
  })
})

describe('SortSelect', () => {
  it('says Sort until a list with no order of its own is sorted', async () => {
    const sort = useSort([{ ip: '10.0.0.1' }], { ip: byAddress((r) => r.ip) })
    const wrapper = mount(SortSelect, { props: { sort, columns: [['ip', 'Address']] } })
    expect(wrapper.get('select').element.value).toBe('')
    expect(wrapper.get('option').text()).toBe('Sort')
    await wrapper.get('select').setValue('ip')
    expect(sort.order.value).toEqual({ by: 'ip', dir: 'asc' })
    expect(wrapper.findAll('option').map((o) => o.text())).toEqual(['Address'])
  })

  it('sorts by the column chosen, in its own order', async () => {
    const wrapper = mount(Host)
    const select = wrapper.get('select')
    expect(select.attributes('aria-label')).toBe('Sort')
    expect(select.element.value).toBe('ip')
    await select.setValue('name')
    expect(header(wrapper, 'Name').attributes('aria-sort')).toBe('ascending')
    expect(ips(wrapper)).toEqual(['10.0.0.3', '10.0.0.20'])
  })
})
